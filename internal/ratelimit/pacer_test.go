package ratelimit

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

func fakePacer() (*Pacer, *time.Time) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	pacer := New()
	pacer.now = func() time.Time { return now }
	pacer.wait = func(ctx context.Context, delay time.Duration) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		now = now.Add(delay)
		return nil
	}
	return pacer, &now
}

func TestPacingSessionRetainsHourlyBudgetAcrossSegments(t *testing.T) {
	pacer, now := fakePacer()
	for range contentHourAllowance - checkpointMutationReserve {
		pacer.charges = append(pacer.charges, charge{at: now.Add(-2 * time.Minute), resource: "rest", points: writePoints, mutation: true})
	}
	path := filepath.Join(t.TempDir(), "session.json")
	if err := pacer.SaveSession(path, "run-1"); err != nil {
		t.Fatal(err)
	}
	restored, restoredNow := fakePacer()
	if err := restored.LoadSession(path, "run-1"); err != nil {
		t.Fatal(err)
	}
	err := restored.Before(context.Background(), Request{Resource: "rest", Points: writePoints, Mutation: true})
	var deferred *DeferredError
	if !errors.As(err, &deferred) || deferred.Reason != "local_hourly_mutations" || !deferred.RetryAt.Equal(now.Add(5*time.Minute)) {
		t.Fatalf("segment reset hourly allowance/cause: %v", err)
	}
	*restoredNow = now.Add(58 * time.Minute)
	if err := restored.Before(context.Background(), Request{Resource: "rest", Points: writePoints, Mutation: true}); err != nil {
		t.Fatal(err)
	}
	if err := New().LoadSession(path, "different-run"); err == nil {
		t.Fatal("session reused across unrelated run")
	}
}

func TestPacerExplainsControllingPrimaryAndServerDeadline(t *testing.T) {
	for _, serverLimited := range []bool{false, true} {
		pacer, now := fakePacer()
		pacer.Seed("rest", 0, 5000, now.Add(10*time.Minute))
		want := "primary_exhausted"
		if serverLimited {
			pacer.pausedUntil = now.Add(20 * time.Minute)
			want = "server_retry_after"
		}
		err := pacer.Before(context.Background(), Request{Resource: "rest", Points: 1})
		var deferred *DeferredError
		if !errors.As(err, &deferred) || deferred.Reason != want {
			t.Fatalf("wrong controlling reason: %v %+v", err, deferred)
		}
	}
}

func TestLocalRechecksDoNotAdmitOrChargeBlockedRequests(t *testing.T) {
	pacer, now := fakePacer()
	for range contentHourAllowance - checkpointMutationReserve {
		pacer.charges = append(pacer.charges, charge{at: now.Add(-2 * time.Minute), resource: "rest", points: writePoints, mutation: true})
	}
	request := Request{Resource: "rest", Points: writePoints, Mutation: true}
	before := len(pacer.charges)
	for range 3 {
		err := pacer.Recheck(context.Background(), request)
		var deferred *DeferredError
		if !errors.As(err, &deferred) || !deferred.RetryAt.Equal(now.Add(5*time.Minute)) || deferred.Reason != "local_hourly_mutations" || len(pacer.charges) != before {
			t.Fatalf("probe admitted/charged blocked work: %v", err)
		}
		*now = deferred.RetryAt
	}
}

func TestLocalRechecksProtectServerAndExhaustedPrimary(t *testing.T) {
	for _, server := range []bool{false, true} {
		pacer, now := fakePacer()
		deadline := now.Add(40 * time.Minute)
		pacer.Seed("rest", 0, 5000, deadline)
		if server {
			pacer.pausedUntil = deadline.Add(5 * time.Minute)
		}
		err := pacer.Recheck(context.Background(), Request{Resource: "rest", Points: 1})
		var deferred *DeferredError
		if !errors.As(err, &deferred) || deferred.RetryAt.Before(deadline) || deferred.ServerLimited != server {
			t.Fatalf("protected deadline shortened: %+v", deferred)
		}
	}
}

func TestPrimaryReserveRecheckRefreshesWithoutCharging(t *testing.T) {
	pacer, now := fakePacer()
	pacer.Seed("rest", 500, 5000, now.Add(time.Hour))
	err := pacer.Recheck(context.Background(), Request{Resource: "rest", Points: 1})
	var deferred *DeferredError
	if !errors.As(err, &deferred) || deferred.Reason != "primary_reserve" || !deferred.RetryAt.Equal(now.Add(5*time.Minute)) {
		t.Fatalf("reserve did not get local probe: %v", err)
	}
	pacer.Seed("rest", 5000, 5000, now.Add(time.Hour))
	if err := pacer.Recheck(context.Background(), Request{Resource: "rest", Points: 1}); err != nil || len(pacer.charges) != 0 {
		t.Fatal("recovered probe failed or charged request")
	}
}

func TestPacerContinuesBeyondOldRunAllowanceAndReplenishes(t *testing.T) {
	pacer, now := fakePacer()
	for range 1000 {
		if err := pacer.Before(context.Background(), Request{Resource: "rest", Points: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if now.Sub(time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)) < time.Minute {
		t.Fatal("rolling window did not pace or replenish")
	}
}

func TestPacerWeightsMutationsAndSharesResourceSpacing(t *testing.T) {
	pacer, now := fakePacer()
	start := *now
	for index := 0; index < 10; index++ {
		resource := "rest"
		if index%2 == 0 {
			resource = "graphql"
		}
		if err := pacer.Before(context.Background(), Request{Resource: resource, Points: 5, Mutation: true}); err != nil {
			t.Fatal(err)
		}
	}
	if now.Sub(start) != 9*time.Second {
		t.Fatal("App mutation spacing was not shared")
	}
	if Classify("POST", "https://api.github.com/graphql", []byte(`{"query":"query { viewer { login } }"}`)).Mutation {
		t.Fatal("GraphQL read counted as content creation")
	}
	if !Classify("POST", "https://api.github.com/graphql", []byte(`{"query":"mutation { operation }"}`)).Mutation {
		t.Fatal("GraphQL mutation was undercounted")
	}
}

func TestPacerDefersPrimaryResetAndHonorsCancellation(t *testing.T) {
	pacer, now := fakePacer()
	header := http.Header{"X-Ratelimit-Remaining": []string{"0"}, "X-Ratelimit-Limit": []string{"5000"}, "X-Ratelimit-Reset": []string{"1791288300"}}
	header.Set("X-RateLimit-Reset", "1791288300")
	pacer.Observe(Request{Resource: "rest"}, header, 429)
	_ = now
	if err := pacer.Before(context.Background(), Request{Resource: "rest", Points: 1}); !errors.Is(err, ErrDeferred) {
		t.Fatalf("reset lost: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := pacer.Before(ctx, Request{Resource: "rest", Points: 1}); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost")
	}
}

func TestPacerExpiredPrimaryRefreshesFromAuthoritativeProbe(t *testing.T) {
	pacer, now := fakePacer()
	pacer.Seed("rest", 0, 5000, now.Add(-time.Second))
	probes := 0
	pacer.SetRefresh(func(ctx context.Context) error {
		probes++
		if err := pacer.Before(ctx, Classify("GET", "/rate_limit", nil)); err != nil {
			return err
		}
		pacer.Seed("rest", 5000, 5000, now.Add(time.Hour))
		return nil
	})
	if err := pacer.Before(context.Background(), Request{Resource: "rest", Points: 1}); err != nil || probes != 1 {
		t.Fatalf("primary reset stalled before quota refresh: %v probes=%d", err, probes)
	}
}

func TestPacerWaitAcrossResetRefreshesBeforeAdmission(t *testing.T) {
	pacer, now := fakePacer()
	start := *now
	pacer.Seed("rest", 0, 5000, now.Add(30*time.Second))
	probes := 0
	pacer.SetRefresh(func(context.Context) error { probes++; pacer.Seed("rest", 5000, 5000, now.Add(time.Hour)); return nil })
	if err := pacer.Before(context.Background(), Request{Resource: "rest", Points: 1}); err != nil || probes != 1 || now.Sub(start) != 30*time.Second {
		t.Fatalf("reset crossing did not refresh/admit: %v probes=%d elapsed=%v", err, probes, now.Sub(start))
	}
}

func TestPacerCheckpointReserveCannotBypassServerThrottle(t *testing.T) {
	pacer, now := fakePacer()
	for index := 0; index < contentHourAllowance-checkpointMutationReserve; index++ {
		pacer.charges = append(pacer.charges, charge{at: now.Add(-2 * time.Minute), resource: "rest", points: 5, mutation: true})
	}
	request := Request{Resource: "rest", Points: 5, Mutation: true}
	if err := pacer.Before(context.Background(), request); !errors.Is(err, ErrDeferred) {
		t.Fatalf("ordinary work consumed cleanup reserve: %v", err)
	}
	if err := pacer.Before(CheckpointContext(context.Background()), request); err != nil {
		t.Fatalf("cleanup reserve unavailable: %v", err)
	}
	pacer.Observe(request, http.Header{"Retry-After": []string{"300"}}, 429)
	if err := pacer.Before(CheckpointContext(context.Background()), request); !errors.Is(err, ErrDeferred) {
		t.Fatal("checkpoint bypassed server pause")
	}
}

func TestPacerHonorsHTTPDateRetryAfter(t *testing.T) {
	pacer, now := fakePacer()
	deadline := now.Add(5 * time.Minute)
	pacer.Observe(Request{Resource: "rest"}, http.Header{"Retry-After": []string{deadline.Format(http.TimeFormat)}}, 429)
	for _, ctx := range []context.Context{context.Background(), CheckpointContext(context.Background())} {
		err := pacer.Before(ctx, Request{Resource: "rest", Points: 1})
		var deferred *DeferredError
		if !errors.As(err, &deferred) || !deferred.RetryAt.Equal(deadline) || !deferred.ServerLimited {
			t.Fatalf("HTTP-date deadline was bypassed: %v", err)
		}
	}
}
