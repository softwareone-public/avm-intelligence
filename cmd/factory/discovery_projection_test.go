package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/github"
)

type scheduledProjectionRoundTripper func(*http.Request) (*http.Response, error)

func (roundTrip scheduledProjectionRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func TestScheduledProjectionClientBootstrapsPacedBudget(t *testing.T) {
	client := scheduledProjectionClient("fixture-token", "fixture-app")
	if client.Pacer == nil {
		t.Fatal("scheduled projection client has no run-local request pacer")
	}
	calls := 0
	client.HTTPClient = &http.Client{Transport: scheduledProjectionRoundTripper(func(request *http.Request) (*http.Response, error) {
		calls++
		if request.URL.Path != "/rate_limit" {
			t.Fatalf("budget bootstrap requested %s, want /rate_limit", request.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"resources":{"core":{"remaining":5000,"limit":5000,"reset":1791734400},"search":{"remaining":30,"limit":30,"reset":1791734400},"graphql":{"remaining":4000,"limit":5000,"reset":1791734400}}}`))}, nil
	})}
	budget, err := client.ReadProjectionBudget(context.Background(), github.ProjectionBudgetPolicy{
		RESTReserve: 50, GraphQLReserve: 100, SearchReserve: 5, SecondaryAllowance: 100, SecondaryReserve: 10,
	})
	if err != nil {
		t.Fatalf("bootstrap scheduled projection budget: %v", err)
	}
	if calls != 1 || budget == nil || budget.RESTRemaining != 5000 || budget.SearchRemaining != 30 || budget.GraphQLRemaining != 4000 {
		t.Fatalf("budget bootstrap calls=%d budget=%+v", calls, budget)
	}
}

func TestProjectionAdmissionWindowRespectsLeaseAndOutcomeReserve(t *testing.T) {
	claimTime := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	if got, want := projectionAdmissionWindow(claimTime, claimTime, time.Time{}), 50*time.Minute; got != want {
		t.Fatalf("admission window with full lease = %s, want %s", got, want)
	}
	if got, want := projectionAdmissionWindow(claimTime, claimTime.Add(time.Minute), time.Time{}), 49*time.Minute; got != want {
		t.Fatalf("admission window after pre-claim work = %s, want %s", got, want)
	}
	if got, want := projectionAdmissionWindow(claimTime, claimTime, claimTime.Add(12*time.Minute)), 12*time.Minute; got != want {
		t.Fatalf("admission window near run deadline = %s, want %s", got, want)
	}
}
