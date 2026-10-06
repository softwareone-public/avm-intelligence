package state

import (
	"context"
	"net/http"
	"time"
)

// RefreshRateLimits reads authoritative App quotas without assuming a reset refilled them.
func (backend GitHubBackend) RefreshRateLimits(ctx context.Context) error {
	var response struct {
		Resources map[string]struct {
			Remaining int   `json:"remaining"`
			Limit     int   `json:"limit"`
			Reset     int64 `json:"reset"`
		} `json:"resources"`
	}
	if err := backend.request(ctx, http.MethodGet, backend.apiRateLimitPath(), nil, &response); err != nil {
		return err
	}
	for _, name := range []string{"core", "graphql", "search"} {
		resource := response.Resources[name]
		key := name
		if key == "core" {
			key = "rest"
		}
		backend.Pacer.Seed(key, resource.Remaining, resource.Limit, time.Unix(resource.Reset, 0))
	}
	return nil
}

func (backend GitHubBackend) apiRateLimitPath() string { return "/rate_limit" }
