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
	if transport.calls != 100 || client.Budget.Snapshot().RESTCalls != 100 {
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
