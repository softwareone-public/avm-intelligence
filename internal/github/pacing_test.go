package github

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/softwareone-public/avm-intelligence/internal/ratelimit"
)

type pacingFixtureTransport struct{ calls int }

func (transport *pacingFixtureTransport) RoundTrip(_ *http.Request) (*http.Response, error) {
	transport.calls++
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
}

func TestPacedProjectionAdmitsBeyondLegacyLocalAllowance(t *testing.T) {
	transport := &pacingFixtureTransport{}
	client := Client{Token: "fixture", HTTPClient: &http.Client{Transport: transport}, Pacer: ratelimit.New(), Budget: &ProjectionBudget{RESTRemaining: 10000, RESTReserve: 500, SecondaryRemaining: 20, SecondaryReserve: 20}}
	for range 100 {
		if err := client.request(context.Background(), http.MethodGet, "https://api.github.com/repos/owner/repo/issues", nil, nil); err != nil {
			t.Fatalf("paced request hit legacy run cap: %v", err)
		}
	}
	snapshot := client.Budget.Snapshot()
	if transport.calls != 100 || snapshot.RESTCalls != 100 || snapshot.SecondaryRemaining != 0 {
		t.Fatal("paced request/accounting count mismatch")
	}
}

func TestPacedMutationRechecksGuardAfterAdmission(t *testing.T) {
	transport := &pacingFixtureTransport{}
	guardCalls := 0
	client := Client{Token: "fixture", HTTPClient: &http.Client{Transport: transport}, Pacer: ratelimit.New(), MutationGuard: func(context.Context) error { guardCalls++; return ErrProjectionFence }}
	err := client.request(context.Background(), http.MethodPatch, "https://api.github.com/repos/owner/repo/issues/1", map[string]string{"title": "fixture"}, nil)
	if !errors.Is(err, ErrProjectionFence) || guardCalls != 1 || transport.calls != 0 {
		t.Fatalf("stale owner sent a mutation: %v guards=%d writes=%d", err, guardCalls, transport.calls)
	}
}

func TestProjectionCancellationBeforeSendIsRetryable(t *testing.T) {
	transport := &pacingFixtureTransport{}
	client := Client{Token: "fixture", HTTPClient: &http.Client{Transport: transport}, Pacer: ratelimit.New()}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := client.request(ctx, http.MethodPatch, "https://api.github.com/repos/owner/repo/issues/1", map[string]string{"title": "fixture"}, nil)
	if err == nil || !errors.Is(err, context.Canceled) || classifyProjectionError(err) != ProjectionErrorTransient || transport.calls != 0 {
		t.Fatalf("pre-send cancellation = %v, class=%q, sends=%d; want retryable and zero sends", err, classifyProjectionError(err), transport.calls)
	}
}

func TestProjectionCancellationAfterTransportStartsNeedsReview(t *testing.T) {
	sends := 0
	client := Client{Token: "fixture", HTTPClient: &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
		sends++
		return nil, context.Canceled
	})}, Pacer: ratelimit.New()}
	err := client.request(context.Background(), http.MethodPatch, "https://api.github.com/repos/owner/repo/issues/1", map[string]string{"title": "fixture"}, nil)
	if err == nil || !errors.Is(err, context.Canceled) || classifyProjectionError(err) != ProjectionErrorNeedsReview || sends != 1 {
		t.Fatalf("in-flight cancellation = %v, class=%q, transport calls=%d; want needs-review after one send", err, classifyProjectionError(err), sends)
	}
}

func TestProjectionBudgetAdmitsOnlyHeadroomAboveEachReserve(t *testing.T) {
	budget := &ProjectionBudget{
		RESTRemaining: 18, RESTReserve: 5, GraphQLRemaining: 9, GraphQLReserve: 2,
		SearchRemaining: 7, SearchReserve: 1, SecondaryRemaining: 6, SecondaryReserve: 2,
	}
	if admitted := budget.AdmitModules(25); admitted != 4 {
		t.Fatalf("admitted modules = %d, want limiting resource headroom 4", admitted)
	}
	budget.SearchRemaining = budget.SearchReserve
	if admitted := budget.AdmitModules(25); admitted != 0 {
		t.Fatalf("admitted modules with exhausted search headroom = %d, want 0", admitted)
	}
	if admitted := budget.AdmitModules(0); admitted != 0 {
		t.Fatalf("admitted modules for empty request = %d, want 0", admitted)
	}
}
