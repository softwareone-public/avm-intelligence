package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/progress"
	"github.com/softwareone-public/avm-intelligence/internal/ratelimit"
)

type ProjectionRequestClass string

const (
	ProjectionREST    ProjectionRequestClass = "rest"
	ProjectionGraphQL ProjectionRequestClass = "graphql"
	ProjectionSearch  ProjectionRequestClass = "search"
	retryAfterHeader                         = "Retry-After"
)

var ErrProjectionBudgetExhausted = errors.New("projection API budget exhausted; checkpoint and resume later")
var ErrGitHubRateLimited = errors.New("GitHub projection rate limit reached; checkpoint and resume later")

// ProjectionPreSendError marks cancellation while the local pacer or owner
// guard is still preventing transmission. The operation is safe to retry.
// Transport errors are deliberately not wrapped because a mutation may have
// reached GitHub before its response was lost.
type ProjectionPreSendError struct{ Cause error }

func (failure *ProjectionPreSendError) Error() string {
	return "projection request cancelled before send"
}
func (failure *ProjectionPreSendError) Unwrap() error { return failure.Cause }

func projectionPreSendError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &ProjectionPreSendError{Cause: err}
	}
	return err
}

// ProjectionBudgetError distinguishes a local reserve from a server-directed
// limit without retaining response bodies or credentials.
type ProjectionBudgetError struct {
	Cause              error
	Resource           ProjectionRequestClass
	Remaining          int
	Reserve            int
	SecondaryRemaining int
	SecondaryReserve   int
	RetryAt            time.Time
	PacingReason       string
}

func (failure *ProjectionBudgetError) Error() string {
	if errors.Is(failure.Cause, ErrGitHubRateLimited) {
		return ErrGitHubRateLimited.Error()
	}
	return "local projection request budget exhausted; checkpoint and resume later"
}

func (failure *ProjectionBudgetError) Unwrap() error { return failure.Cause }

// Is preserves the existing bounded-stop contract for both types of budget failure.
func (failure *ProjectionBudgetError) Is(target error) bool {
	return target == ErrProjectionBudgetExhausted || errors.Is(failure.Cause, target)
}

// ProjectionBudget tracks separate primary resources and one shared secondary
// reserve. It is scoped to one bounded projection run.
type ProjectionBudget struct {
	mu sync.Mutex

	RESTRemaining      int
	RESTReserve        int
	GraphQLRemaining   int
	GraphQLReserve     int
	SearchRemaining    int
	SearchReserve      int
	SecondaryRemaining int
	SecondaryReserve   int
	PausedUntil        time.Time
	ServerLimited      bool
	GraphQLResetAt     time.Time
	Calls              map[ProjectionRequestClass]int
}

// ProjectionBudgetPolicy reserves headroom for follow-up reads and retries.
type ProjectionBudgetPolicy struct {
	RESTReserve        int
	GraphQLReserve     int
	SearchReserve      int
	SecondaryAllowance int
	SecondaryReserve   int
}

// ProjectionEffectCost estimates the per-module request and mutation profile
// used before claiming a delivery. The shared Pacer remains authoritative for
// each actual request because remote labels, retries, and page counts can vary.
type ProjectionEffectCost struct {
	RESTRequests      int
	GraphQLRequests   int
	SearchRequests    int
	SecondaryRequests int
	Mutations         int
	EstimatedDuration time.Duration
}

// AdmitEffect checks primary and secondary reserve headroom plus the bounded
// runtime needed for the estimated effects. It does not consume budget; actual
// requests are charged by the client Pacer after the module is claimed.
func (budget *ProjectionBudget) AdmitEffect(cost ProjectionEffectCost, available time.Duration) bool {
	if budget == nil || cost.RESTRequests < 0 || cost.GraphQLRequests < 0 || cost.SearchRequests < 0 || cost.SecondaryRequests < 0 || cost.Mutations < 0 || cost.EstimatedDuration < 0 {
		return false
	}
	if available <= 0 || cost.EstimatedDuration > available {
		return false
	}
	budget.mu.Lock()
	defer budget.mu.Unlock()
	return budget.RESTRemaining-budget.RESTReserve >= cost.RESTRequests &&
		budget.GraphQLRemaining-budget.GraphQLReserve >= cost.GraphQLRequests &&
		budget.SearchRemaining-budget.SearchReserve >= cost.SearchRequests &&
		budget.SecondaryRemaining-budget.SecondaryReserve >= cost.SecondaryRequests
}

// AdmitModules returns a conservative upper bound for a batch using the
// currently available primary and secondary request headroom. Each admitted
// module reserves room for at least one request in every resource class; the
// client still enforces the full per-request reserve while processing.
func (budget *ProjectionBudget) AdmitModules(requested int) int {
	if budget == nil || requested <= 0 {
		return 0
	}
	budget.mu.Lock()
	defer budget.mu.Unlock()
	available := []int{
		budget.RESTRemaining - budget.RESTReserve,
		budget.GraphQLRemaining - budget.GraphQLReserve,
		budget.SearchRemaining - budget.SearchReserve,
		budget.SecondaryRemaining - budget.SecondaryReserve,
	}
	admitted := requested
	for _, count := range available {
		if count < admitted {
			admitted = count
		}
	}
	if admitted < 0 {
		return 0
	}
	return admitted
}

// ReadProjectionBudget bootstraps separate primary limits from GitHub's
// rate-limit endpoint. The lookup is performed only for a non-empty batch.
func (client Client) ReadProjectionBudget(ctx context.Context, policy ProjectionBudgetPolicy) (*ProjectionBudget, error) {
	if policy.RESTReserve < 0 || policy.GraphQLReserve < 0 || policy.SearchReserve < 0 || policy.SecondaryReserve < 0 || policy.SecondaryAllowance <= policy.SecondaryReserve {
		return nil, fmt.Errorf("projection budget policy and secondary reserve are invalid")
	}
	lookup := client
	lookup.Budget = nil
	var response struct {
		Resources struct {
			Core struct {
				Remaining int   `json:"remaining"`
				Limit     int   `json:"limit"`
				Reset     int64 `json:"reset"`
			} `json:"core"`
			Search struct {
				Remaining int   `json:"remaining"`
				Limit     int   `json:"limit"`
				Reset     int64 `json:"reset"`
			} `json:"search"`
			GraphQL struct {
				Remaining int   `json:"remaining"`
				Limit     int   `json:"limit"`
				Reset     int64 `json:"reset"`
			} `json:"graphql"`
		} `json:"resources"`
	}
	if err := lookup.rest(ctx, http.MethodGet, "/rate_limit", nil, &response); err != nil {
		return nil, fmt.Errorf("read GitHub projection rate limits: %w", err)
	}
	if response.Resources.Core.Remaining < 0 || response.Resources.Search.Remaining < 0 || response.Resources.GraphQL.Remaining < 0 {
		return nil, fmt.Errorf("GitHub rate-limit response omitted a required resource")
	}
	client.Pacer.Seed("rest", response.Resources.Core.Remaining, response.Resources.Core.Limit, time.Unix(response.Resources.Core.Reset, 0))
	client.Pacer.Seed("graphql", response.Resources.GraphQL.Remaining, response.Resources.GraphQL.Limit, time.Unix(response.Resources.GraphQL.Reset, 0))
	client.Pacer.Seed("search", response.Resources.Search.Remaining, response.Resources.Search.Limit, time.Unix(response.Resources.Search.Reset, 0))
	return &ProjectionBudget{
		// The legacy counter remains telemetry-only when a shared pacer is supplied.
		RESTRemaining: response.Resources.Core.Remaining, RESTReserve: policy.RESTReserve,
		GraphQLRemaining: response.Resources.GraphQL.Remaining, GraphQLReserve: policy.GraphQLReserve,
		SearchRemaining: response.Resources.Search.Remaining, SearchReserve: policy.SearchReserve,
		SecondaryRemaining: policy.SecondaryAllowance - 1, SecondaryReserve: policy.SecondaryReserve,
		Calls: map[ProjectionRequestClass]int{ProjectionREST: 1},
	}, nil
}

func pacedProjectionFailure(err error, class ProjectionRequestClass) error {
	var deferred *ratelimit.DeferredError
	if !errors.As(err, &deferred) {
		return err
	}
	cause := err
	if deferred.ServerLimited {
		cause = errors.Join(ErrGitHubRateLimited, err)
	}
	return &ProjectionBudgetError{Cause: cause, Resource: class, RetryAt: deferred.RetryAt, PacingReason: deferred.Reason}
}

func (budget *ProjectionBudget) recordPacedCall(class ProjectionRequestClass) {
	if budget == nil {
		return
	}
	budget.mu.Lock()
	defer budget.mu.Unlock()
	if budget.Calls == nil {
		budget.Calls = map[ProjectionRequestClass]int{}
	}
	budget.Calls[class]++
	if budget.SecondaryRemaining > 0 {
		budget.SecondaryRemaining--
	}
}

// Take reserves one request before any network call. Retries consume budget too.
func (budget *ProjectionBudget) Take(class ProjectionRequestClass, now time.Time) error {
	if budget == nil {
		return nil
	}
	budget.mu.Lock()
	defer budget.mu.Unlock()
	if now.IsZero() || (!budget.PausedUntil.IsZero() && now.Before(budget.PausedUntil)) {
		cause := ErrProjectionBudgetExhausted
		if !budget.PausedUntil.IsZero() && now.Before(budget.PausedUntil) {
			cause = ErrGitHubRateLimited
		}
		return budget.failureLocked(class, cause)
	}
	remaining, reserve := budget.resource(class)
	if remaining <= reserve || budget.SecondaryRemaining <= budget.SecondaryReserve {
		if budget.ServerLimited {
			return budget.failureLocked(class, ErrGitHubRateLimited)
		}
		return budget.failureLocked(class, ErrProjectionBudgetExhausted)
	}
	budget.setResource(class, remaining-1)
	budget.SecondaryRemaining--
	if budget.Calls == nil {
		budget.Calls = make(map[ProjectionRequestClass]int)
	}
	budget.Calls[class]++
	return nil
}

func (budget *ProjectionBudget) failureLocked(class ProjectionRequestClass, cause error) *ProjectionBudgetError {
	remaining, reserve := budget.resource(class)
	return &ProjectionBudgetError{Cause: cause, Resource: class, Remaining: remaining, Reserve: reserve,
		SecondaryRemaining: budget.SecondaryRemaining, SecondaryReserve: budget.SecondaryReserve, RetryAt: budget.PausedUntil}
}

func (budget *ProjectionBudget) serverLimit(class ProjectionRequestClass) error {
	budget.mu.Lock()
	defer budget.mu.Unlock()
	return budget.failureLocked(class, ErrGitHubRateLimited)
}

func (budget *ProjectionBudget) graphServerLimit() error {
	if budget == nil {
		return &ProjectionBudgetError{Cause: ErrGitHubRateLimited, Resource: ProjectionGraphQL}
	}
	budget.mu.Lock()
	defer budget.mu.Unlock()
	budget.ServerLimited = true
	budget.SecondaryRemaining = 0
	if budget.GraphQLResetAt.After(budget.PausedUntil) {
		budget.PausedUntil = budget.GraphQLResetAt
	}
	return budget.failureLocked(ProjectionGraphQL, ErrGitHubRateLimited)
}

// warnProjectionBudget identifies local guards and server limits without logging response bodies.
func warnProjectionBudget(ctx context.Context, err error, statusCode int) {
	reason := "local_projection_budget_exhausted"
	if errors.Is(err, ErrGitHubRateLimited) {
		reason = "github_rate_limited"
	}
	var paced *ratelimit.DeferredError
	if errors.As(err, &paced) && !paced.ServerLimited {
		reason = "local_pacing_limit"
	}
	attributes := []any{"reason", reason}
	if statusCode != 0 {
		attributes = append(attributes, "status", statusCode)
	}
	var failure *ProjectionBudgetError
	if errors.As(err, &failure) {
		attributes = append(attributes, "resource", failure.Resource)
		if paced != nil {
			attributes = append(attributes, "pacing_reason", failure.PacingReason)
		} else {
			attributes = append(attributes, "remaining", failure.Remaining, "reserve", failure.Reserve, "secondary_remaining", failure.SecondaryRemaining, "secondary_reserve", failure.SecondaryReserve)
		}
		if !failure.RetryAt.IsZero() {
			attributes = append(attributes, "retry_at", failure.RetryAt.UTC())
		}
	}
	progress.Logger(ctx).WarnContext(ctx, "projection request paused", attributes...)
}

// Observe updates primary headroom and pauses on primary or secondary limits.
func (budget *ProjectionBudget) Observe(class ProjectionRequestClass, headers http.Header, statusCode int, now time.Time) {
	if budget == nil {
		return
	}
	budget.mu.Lock()
	defer budget.mu.Unlock()
	if class == ProjectionGraphQL {
		budget.GraphQLResetAt = projectionServerDeadline(headers, now)
	}
	resource := headers.Get("X-RateLimit-Resource")
	if resourceClass, ok := resourceClass(resource, class); ok {
		if remaining, err := strconv.Atoi(headers.Get("X-RateLimit-Remaining")); err == nil && remaining >= 0 {
			budget.setResource(resourceClass, remaining)
		}
	}
	if statusCode != http.StatusTooManyRequests && !(statusCode == http.StatusForbidden && isRateLimited(headers)) {
		return
	}
	budget.SecondaryRemaining = 0
	budget.ServerLimited = true
	if seconds, err := strconv.Atoi(headers.Get(retryAfterHeader)); err == nil && seconds > 0 {
		budget.PausedUntil = now.Add(time.Duration(seconds) * time.Second)
		return
	}
	if reset, err := strconv.ParseInt(headers.Get("X-RateLimit-Reset"), 10, 64); err == nil && reset > now.Unix() {
		budget.PausedUntil = time.Unix(reset, 0)
	}
}

func projectionServerDeadline(headers http.Header, now time.Time) time.Time {
	if seconds, err := strconv.Atoi(headers.Get(retryAfterHeader)); err == nil && seconds > 0 {
		return now.Add(time.Duration(seconds) * time.Second)
	}
	if reset, err := strconv.ParseInt(headers.Get("X-RateLimit-Reset"), 10, 64); err == nil && reset > now.Unix() {
		return time.Unix(reset, 0).UTC()
	}
	return time.Time{}
}

// Snapshot returns a value-only budget checkpoint suitable for durable batches.
func (budget *ProjectionBudget) Snapshot() BudgetSnapshot {
	if budget == nil {
		return BudgetSnapshot{}
	}
	budget.mu.Lock()
	defer budget.mu.Unlock()
	return BudgetSnapshot{
		RESTRemaining: budget.RESTRemaining, GraphQLRemaining: budget.GraphQLRemaining,
		SearchRemaining: budget.SearchRemaining, SecondaryRemaining: budget.SecondaryRemaining,
		RESTCalls: budget.Calls[ProjectionREST], GraphQLCalls: budget.Calls[ProjectionGraphQL],
		SearchCalls: budget.Calls[ProjectionSearch],
	}
}

// ResumeAt returns the server-directed pause deadline, if one was observed.
func (budget *ProjectionBudget) ResumeAt() time.Time {
	if budget == nil {
		return time.Time{}
	}
	budget.mu.Lock()
	defer budget.mu.Unlock()
	return budget.PausedUntil
}

func classifyProjectionRequest(endpoint string) ProjectionRequestClass {
	if strings.Contains(endpoint, "/search/") {
		return ProjectionSearch
	}
	if strings.Contains(endpoint, "/graphql") {
		return ProjectionGraphQL
	}
	return ProjectionREST
}

func resourceClass(resource string, fallback ProjectionRequestClass) (ProjectionRequestClass, bool) {
	switch resource {
	case "core":
		return ProjectionREST, true
	case "graphql":
		return ProjectionGraphQL, true
	case "search":
		return ProjectionSearch, true
	default:
		return fallback, resource == ""
	}
}

func (budget *ProjectionBudget) resource(class ProjectionRequestClass) (int, int) {
	switch class {
	case ProjectionREST:
		return budget.RESTRemaining, budget.RESTReserve
	case ProjectionGraphQL:
		return budget.GraphQLRemaining, budget.GraphQLReserve
	case ProjectionSearch:
		return budget.SearchRemaining, budget.SearchReserve
	default:
		return 0, 0
	}
}

func (budget *ProjectionBudget) setResource(class ProjectionRequestClass, remaining int) {
	switch class {
	case ProjectionREST:
		budget.RESTRemaining = remaining
	case ProjectionGraphQL:
		budget.GraphQLRemaining = remaining
	case ProjectionSearch:
		budget.SearchRemaining = remaining
	}
}

func isRateLimited(headers http.Header) bool {
	if headers.Get(retryAfterHeader) != "" {
		return true
	}
	remaining, err := strconv.Atoi(headers.Get("X-RateLimit-Remaining"))
	return err == nil && remaining == 0
}
