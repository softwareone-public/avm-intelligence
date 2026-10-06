package state

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStateAPIFailureRetainsSafeStageThroughWrapping(t *testing.T) {
	for _, operation := range []struct{ method, endpoint, name string }{
		{http.MethodPost, "/git/trees", "create_tree"},
		{http.MethodPost, "/git/commits", "create_commit"},
		{http.MethodPatch, "/git/refs/heads/factory-state", "update_ref"},
	} {
		t.Run(operation.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.Header().Set("X-GitHub-Request-Id", "ABCD:0123:4567:89EF")
				response.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = response.Write([]byte(`{"message":"PRIVATE_API_BODY_SENTINEL"}`))
			}))
			defer server.Close()
			backend := GitHubBackend{Owner: "owner", Repository: "repo", Branch: "factory-state", Token: "PRIVATE_TOKEN_SENTINEL", Endpoint: server.URL}
			err := backend.request(context.Background(), operation.method, backend.repositoryPath(operation.endpoint), nil, nil)
			wrapped := fmt.Errorf("commit state snapshot: %w", err)
			var failure *APIError
			if !errors.As(wrapped, &failure) || failure.operation != operation.name || failure.status != 422 || failure.requestID != "ABCD:0123:4567:89EF" {
				t.Fatalf("missing safe operation metadata: %v", wrapped)
			}
			logged := fmt.Sprint(failure.DiagnosticAttributes(), failure.Error())
			if strings.Contains(logged, "PRIVATE_") || strings.Contains(logged, server.URL) {
				t.Fatal("safe diagnostics exposed private input or API body")
			}
		})
	}
}

func TestStateAPIFailureRejectsUntrustedRequestIDAndPreservesCancellation(t *testing.T) {
	header := http.Header{"X-Github-Request-Id": []string{"PRIVATE_HEADER_SENTINEL"}}
	failure := stateAPIFailure(http.MethodPost, "/git/trees", 0, header, context.Canceled)
	if failure.requestID != "" || !errors.Is(failure, context.Canceled) {
		t.Fatal("request ID privacy or cancellation identity was lost")
	}
}

func TestStateAPIValidationDetailsAreAllowlisted(t *testing.T) {
	failure := stateAPIFailure(http.MethodPost, "/git/trees", 422, nil, nil)
	failure.captureValidation([]byte(`{"message":"PRIVATE_MESSAGE","errors":[{"resource":"Tree","field":"sha","code":"invalid","value":"PRIVATE_VALUE","message":"PRIVATE_REASON"},{"resource":"PRIVATE_RESOURCE","field":"PRIVATE_FIELD","code":"PRIVATE_CODE"}]}`))
	attributes := fmt.Sprint(failure.DiagnosticAttributes())
	if !strings.Contains(attributes, "validation_resources Tree") || !strings.Contains(attributes, "validation_fields sha") || !strings.Contains(attributes, "validation_codes invalid") || strings.Contains(attributes, "PRIVATE_") {
		t.Fatalf("validation diagnostics lost enums or exposed raw data: %s", attributes)
	}
}

type stateFailureTransport struct {
	response *http.Response
	err      error
}

func (transport stateFailureTransport) RoundTrip(_ *http.Request) (*http.Response, error) {
	return transport.response, transport.err
}

type stateFailureBody struct{}

func (stateFailureBody) Read(_ []byte) (int, error) { return 0, fmt.Errorf("PRIVATE_READ_SENTINEL") }
func (stateFailureBody) Close() error               { return nil }

func TestStateAPIResponseAndTransportFailuresRemainActionableAndPrivate(t *testing.T) {
	for _, reason := range []string{"transport_failure", "response_read_failure", "response_decode_failure"} {
		t.Run(reason, func(t *testing.T) {
			transport := stateFailureTransport{response: &http.Response{StatusCode: http.StatusCreated, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("PRIVATE_MALFORMED_RESPONSE"))}}
			if reason == "transport_failure" {
				transport.response, transport.err = nil, context.Canceled
			} else if reason == "response_read_failure" {
				transport.response.Body = stateFailureBody{}
			}
			backend := GitHubBackend{Owner: "owner", Repository: "repo", Branch: "factory-state", Token: "PRIVATE_TOKEN_SENTINEL", HTTPClient: &http.Client{Transport: transport}}
			var result struct {
				SHA string `json:"sha"`
			}
			err := backend.request(context.Background(), http.MethodPost, backend.repositoryPath("/git/trees"), nil, &result)
			var failure *APIError
			if !errors.As(err, &failure) || failure.operation != "create_tree" || failure.reason != reason {
				t.Fatalf("failure lost safe stage/category: %v", err)
			}
			if reason == "transport_failure" && !errors.Is(err, context.Canceled) {
				t.Fatal("transport cancellation identity was lost")
			}
			logged := fmt.Sprint(failure.Error(), failure.DiagnosticAttributes())
			if strings.Contains(logged, "PRIVATE_") || strings.Contains(logged, "https://") {
				t.Fatal("response/transport diagnostics exposed private content")
			}
		})
	}
}
