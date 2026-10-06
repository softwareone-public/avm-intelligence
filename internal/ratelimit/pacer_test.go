package ratelimit

import (
	"context"
	"errors"
	"net/http"
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
