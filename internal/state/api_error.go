package state

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
)

var githubRequestIDPattern = regexp.MustCompile(`^[0-9A-Fa-f:]{1,128}$`)

// APIError exposes allowlisted state API diagnostics without exposing response bodies or URLs.
type APIError struct {
	operation           string
	status              int
	requestID           string
	reason              string
	cause               error
	validationResources string
	validationFields    string
	validationCodes     string
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
	if failure.validationResources != "" {
		attributes = append(attributes, "validation_resources", failure.validationResources)
	}
	if failure.validationFields != "" {
		attributes = append(attributes, "validation_fields", failure.validationFields)
	}
	if failure.validationCodes != "" {
		attributes = append(attributes, "validation_codes", failure.validationCodes)
	}
	return attributes
}

// captureValidation keeps only documented fixed validation enums, never messages or values.
func (failure *APIError) captureValidation(content []byte) {
	if failure.status != http.StatusUnprocessableEntity {
		return
	}
	var body struct {
		Errors []struct {
			Resource string `json:"resource"`
			Field    string `json:"field"`
			Code     string `json:"code"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(content, &body); err != nil {
		return
	}
	resources, fields, codes := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, detail := range body.Errors {
		switch detail.Resource {
		case "Tree", "Blob", "Commit", "Reference":
			resources[detail.Resource] = true
		}
		switch detail.Field {
		case "tree", "sha", "path", "mode", "type", "content", "base_tree", "parents", "ref", "force":
			fields[detail.Field] = true
		}
		switch detail.Code {
		case "missing", "missing_field", "invalid", "already_exists", "unprocessable", "custom":
			codes[detail.Code] = true
		}
	}
	failure.validationResources = validationEnumList(resources)
	failure.validationFields = validationEnumList(fields)
	failure.validationCodes = validationEnumList(codes)
}

func validationEnumList(values map[string]bool) string {
	ordered := make([]string, 0, len(values))
	for value := range values {
		ordered = append(ordered, value)
	}
	sort.Strings(ordered)
	return strings.Join(ordered, ",")
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
