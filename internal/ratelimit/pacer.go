package ratelimit

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/progress"
)

const (
	minuteWindow              = time.Minute
	hourWindow                = time.Hour
	mutationSpacing           = time.Second
	maximumWait               = time.Minute
	restPointAllowance        = 720
	graphPointAllowance       = 1600
	contentMinuteAllowance    = 64
	contentHourAllowance      = 400
	checkpointMutationReserve = 12
	primaryReservePercent     = 20
	writePoints               = 5
)

var ErrDeferred = errors.New("GitHub request deferred beyond safe wait bound")

// DeferredError gives the orchestrator an exact deadline for durable continuation.
type DeferredError struct {
	ServerLimited bool
	Resource      string
	RetryAt       time.Time
}

func (failure *DeferredError) Error() string {
	return "GitHub request deferred; preserve checkpoint and resume later"
}
func (failure *DeferredError) Unwrap() error { return ErrDeferred }

type charge struct {
	at       time.Time
	resource string
	points   int
	mutation bool
}
type primary struct {
	remaining, limit int
	reset            time.Time
}

// Pacer coordinates conservative rolling windows across state and projection clients.
type Pacer struct {
	refresh       func(context.Context) error
	mu            sync.Mutex
	charges       []charge
	primary       map[string]primary
	pausedUntil   time.Time
	lastMutation  time.Time
	deferredUntil time.Time
	now           func() time.Time
	wait          func(context.Context, time.Duration) error
}

// New returns a run-local shared gate; all waits respect context cancellation.
func New() *Pacer { return &Pacer{primary: map[string]primary{}, now: time.Now, wait: waitContext} }

func waitContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Request contains cost metadata only, never tokens, URLs or payload contents.
type Request struct {
	Resource                      string
	Points                        int
	Mutation, Checkpoint, Refresh bool
}

// Classify conservatively treats unknown GraphQL operations as mutations.
func Classify(method, endpoint string, encoded []byte) Request {
	request := Request{Resource: "rest", Points: 1, Refresh: method == http.MethodGet && strings.HasSuffix(endpoint, "/rate_limit")}
	if strings.Contains(endpoint, "/search/") {
		request.Resource = "search"
	}
	request.Mutation = method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
	if strings.Contains(endpoint, "/graphql") {
		request.Resource = "graphql"
		var payload struct {
			Query string `json:"query"`
		}
		if json.Unmarshal(encoded, &payload) == nil {
			request.Mutation = !graphRead(payload.Query)
		}
	}
	if request.Mutation {
		request.Points = writePoints
	}
	return request
}

func graphRead(query string) bool {
	query = strings.TrimSpace(query)
	for strings.HasPrefix(query, "#") {
		_, rest, found := strings.Cut(query, "\n")
		if !found {
			return false
		}
		query = strings.TrimSpace(rest)
	}
	if strings.HasPrefix(query, "{") {
		return true
	}
	if !strings.HasPrefix(query, "query") {
		return false
	}
	rest := strings.TrimPrefix(query, "query")
	return rest == "" || strings.ContainsRune(" \t\r\n(", rune(rest[0]))
}

// Before admits a request or parks it without replaying an external side effect.
func (pacer *Pacer) Before(ctx context.Context, request Request) error {
	if pacer == nil {
		return nil
	}
	request.Checkpoint, _ = ctx.Value(checkpointKey{}).(bool)
	started := pacer.now()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := pacer.refreshPrimary(ctx, request); err != nil {
			return err
		}
		pacer.mu.Lock()
		now := pacer.now()
		due := pacer.dueLocked(request, now)
		if !due.After(now) {
			pacer.charges = append(pacer.charges, charge{at: now, resource: request.Resource, points: request.Points, mutation: request.Mutation})
			if request.Mutation {
				pacer.lastMutation = now
			}
			if state, found := pacer.primary[request.Resource]; found && !request.Refresh {
				state.remaining--
				pacer.primary[request.Resource] = state
			}
			pacer.mu.Unlock()
			return nil
		}
		pacer.mu.Unlock()
		deadline, bounded := ctx.Deadline()
		if due.Sub(started) > maximumWait || bounded && !due.Before(deadline) {
			pacer.mu.Lock()
			pacer.deferredUntil = later(pacer.deferredUntil, due)
			pacer.mu.Unlock()
			pacer.mu.Lock()
			serverLimited := pacer.pausedUntil.After(now)
			pacer.mu.Unlock()
			return &DeferredError{Resource: request.Resource, RetryAt: due, ServerLimited: serverLimited}
		}
		progress.Logger(ctx).InfoContext(ctx, "GitHub request paced", "resource", request.Resource, "wait", due.Sub(now), "retry_at", due.UTC())
		if err := pacer.wait(ctx, due.Sub(now)); err != nil {
			return err
		}
	}
}

func (pacer *Pacer) refreshPrimary(ctx context.Context, request Request) error {
	if request.Refresh {
		return nil
	}
	pacer.mu.Lock()
	state, known := pacer.primary[request.Resource]
	refresh := pacer.refresh
	needsRefresh := !known || !pacer.now().Before(state.reset)
	pacer.mu.Unlock()
	if needsRefresh && refresh != nil {
		return refresh(ctx)
	}
	return nil
}

func later(left, right time.Time) time.Time {
	if right.After(left) {
		return right
	}
	return left
}
func samePointResource(left, right string) bool { return (left == "graphql") == (right == "graphql") }

func (pacer *Pacer) dueLocked(request Request, now time.Time) time.Time {
	due := later(now, pacer.pausedUntil)
	kept := pacer.charges[:0]
	for _, item := range pacer.charges {
		if now.Sub(item.at) < hourWindow {
			kept = append(kept, item)
		}
	}
	pacer.charges = kept
	points, minuteMutations, hourMutations := 0, 0, 0
	for _, item := range kept {
		if item.mutation {
			hourMutations++
		}
		if now.Sub(item.at) < minuteWindow {
			if samePointResource(item.resource, request.Resource) {
				points += item.points
			}
			if item.mutation {
				minuteMutations++
			}
		}
	}
	pointAllowance := restPointAllowance
	if request.Resource == "graphql" {
		pointAllowance = graphPointAllowance
	}
	minuteAllowance, hourAllowance := contentMinuteAllowance, contentHourAllowance
	if !request.Checkpoint {
		minuteAllowance -= checkpointMutationReserve
		hourAllowance -= checkpointMutationReserve
	}
	for _, item := range kept {
		if points+request.Points > pointAllowance && now.Sub(item.at) < minuteWindow && samePointResource(item.resource, request.Resource) {
			due = later(due, item.at.Add(minuteWindow))
			points -= item.points
		}
		if request.Mutation && item.mutation {
			if hourMutations >= hourAllowance {
				due = later(due, item.at.Add(hourWindow))
				hourMutations--
			}
			if minuteMutations >= minuteAllowance && now.Sub(item.at) < minuteWindow {
				due = later(due, item.at.Add(minuteWindow))
				minuteMutations--
			}
		}
	}
	if request.Mutation {
		due = later(due, pacer.lastMutation.Add(mutationSpacing))
	}
	if state, found := pacer.primary[request.Resource]; !request.Refresh && found && (state.remaining <= 0 || !request.Checkpoint && state.remaining <= state.limit*primaryReservePercent/100) {
		if state.reset.After(now) {
			due = later(due, state.reset)
		} else {
			due = later(due, now.Add(maximumWait+time.Second))
		}
	}
	return due
}

// Observe tracks authoritative primary windows and actual server throttles.
func (pacer *Pacer) Observe(request Request, header http.Header, status int) {
	if pacer == nil {
		return
	}
	pacer.mu.Lock()
	defer pacer.mu.Unlock()
	now := pacer.now()
	resource := request.Resource
	if header.Get("X-RateLimit-Resource") == "core" {
		resource = "rest"
	} else if value := header.Get("X-RateLimit-Resource"); value == "graphql" || value == "search" {
		resource = value
	}
	remaining, remainingErr := strconv.Atoi(header.Get("X-RateLimit-Remaining"))
	limit, limitErr := strconv.Atoi(header.Get("X-RateLimit-Limit"))
	reset, resetErr := strconv.ParseInt(header.Get("X-RateLimit-Reset"), 10, 64)
	if remainingErr == nil && limitErr == nil && resetErr == nil && remaining >= 0 && limit > 0 {
		pacer.primary[resource] = primary{remaining: remaining, limit: limit, reset: time.Unix(reset, 0)}
	}
	if status != 429 && !(status == 403 && (remainingErr == nil && remaining == 0 || header.Get("Retry-After") != "")) {
		return
	}
	due := now.Add(time.Minute)
	if remainingErr == nil && remaining == 0 && resetErr == nil {
		due = time.Unix(reset, 0)
	}
	if seconds, err := strconv.Atoi(header.Get("Retry-After")); err == nil && seconds > 0 {
		due = now.Add(time.Duration(seconds) * time.Second)
	} else if deadline, err := http.ParseTime(header.Get("Retry-After")); err == nil && deadline.After(now) {
		due = deadline
	}
	pacer.pausedUntil = later(pacer.pausedUntil, due)
}

// DeferServer handles GraphQL HTTP-200 throttles without replaying the rejected operation.
func (pacer *Pacer) DeferServer(resource string) error {
	pacer.mu.Lock()
	defer pacer.mu.Unlock()
	due := later(pacer.now().Add(time.Minute), pacer.pausedUntil)
	if state, found := pacer.primary[resource]; found && state.remaining <= state.limit*primaryReservePercent/100 && state.reset.After(due) {
		due = state.reset
	}
	pacer.pausedUntil = due
	return &DeferredError{Resource: resource, RetryAt: due, ServerLimited: true}
}
