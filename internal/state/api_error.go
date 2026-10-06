package state

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

var githubRequestIDPattern = regexp.MustCompile(`^[0-9A-Fa-f:]{1,128}$`)

// APIError exposes allowlisted state API diagnostics without exposing response bodies or URLs.
type APIError struct {
	operation string
	status    int
	requestID string
	reason    string
	cause     error
}

func (failure *APIError) Error() string {
	return fmt.Sprintf("GitHub state operation %s failed (HTTP %d, %s)", failure.operation, failure.status, failure.reason)
}

// Unwrap preserves cancellation and transport causes for callers, not log rendering.
func (failure *APIError) Unwrap() error { return failure.cause }

// DiagnosticAttributes contains only reviewed operation names, status and validated request IDs.
func (failure *APIError) DiagnosticAttributes() []any {
	attributes := []any{"component", "state", "operation", failure.operation, "http_status", failure.status, "reason", failure.reason}
	if failure.requestID != "" {
		attributes = append(attributes, "github_request_id", failure.requestID)
	}
	return attributes
}

func stateAPIFailure(method, apiPath string, status int, header http.Header, cause error) *APIError {
	failure := &APIError{operation: stateAPIOperation(method, apiPath), status: status, reason: "unexpected_response", cause: cause}
	switch {
	case status == 0:
		failure.reason = "transport_failure"
	case status == http.StatusUnauthorized:
		failure.reason = "authentication_failed"
	case status == http.StatusForbidden && header.Get("X-RateLimit-Remaining") == "0":
		failure.reason = "primary_rate_limit"
	case status == http.StatusForbidden:
		failure.reason = "forbidden"
	case status == http.StatusNotFound:
		failure.reason = "not_found"
	case status == http.StatusConflict:
		failure.reason = "conflict"
	case status == http.StatusUnprocessableEntity:
		failure.reason = "validation_failed"
	case status == http.StatusTooManyRequests:
		failure.reason = "rate_limited"
	case status >= http.StatusInternalServerError:
		failure.reason = "server_failure"
	}
	requestID := header.Get("X-GitHub-Request-Id")
	if githubRequestIDPattern.MatchString(requestID) {
		failure.requestID = requestID
	}
	return failure
}

func stateAPIOperation(method, apiPath string) string {
	switch {
	case method == http.MethodPost && strings.HasSuffix(apiPath, "/git/trees"):
		return "create_tree"
	case method == http.MethodPost && strings.HasSuffix(apiPath, "/git/commits"):
		return "create_commit"
	case method == http.MethodPatch && strings.Contains(apiPath, "/git/refs/heads/"):
		return "update_ref"
	case method == http.MethodGet && strings.Contains(apiPath, "/git/ref/heads/"):
		return "read_ref"
	case method == http.MethodGet && strings.Contains(apiPath, "/git/commits/"):
		return "read_commit"
	case method == http.MethodGet && strings.Contains(apiPath, "/git/trees/"):
		return "read_tree"
	case method == http.MethodGet && strings.Contains(apiPath, "/git/blobs/"):
		return "read_blob"
	default:
		return "state_request"
	}
}
