package github

import (
	"fmt"
	"time"
)

const projectionRequestTimeoutEstimate = 30 * time.Second

// EstimateProjectionEffectCost gives singleton admission a per-module profile
// rather than the old one-request-per-resource floor. It reserves the current
// Issue/label path, up to the bounded managed-label cleanup ceiling, all desired
// Project fields, and two REST operations for each pending run-evidence comment. The runtime estimate includes the default
// request timeout and mutation spacing. Remote snapshot pagination and label
// cleanup can add work; the run-local Pacer is still the final request gate.
func EstimateProjectionEffectCost(intent ProjectionIntentRecord) (ProjectionEffectCost, error) {
	if err := intent.Validate(); err != nil {
		return ProjectionEffectCost{}, fmt.Errorf("estimate projection effect cost: %w", err)
	}
	fields := len(desiredProjectFieldValues(intent.Projection))
	evidence := len(intent.PendingEvidence)
	cost := ProjectionEffectCost{
		// Each evidence marker lookup may scan the reviewed comment-page ceiling,
		// followed by at most one create or update for that evidence item.
		RESTRequests: 12 + maxManagedLabelCleanupPerIssue + (maxEvidenceCommentPages+1)*evidence,
		// Reserve the cold-cache page ceiling for the Project item snapshot,
		// plus identity/field reads, an item create, and the Issue summary write.
		GraphQLRequests: 13 + maxProjectionProjectItemPages + fields,
		// The exact Issue-marker index is paginated at 100 results and capped
		// at GitHub Search's 1,000-result boundary.
		SearchRequests: 10,
		Mutations:      4 + maxManagedLabelCleanupPerIssue + fields + evidence,
	}
	cost.SecondaryRequests = cost.RESTRequests + cost.GraphQLRequests + cost.SearchRequests
	cost.EstimatedDuration = time.Duration(cost.SecondaryRequests)*projectionRequestTimeoutEstimate + time.Duration(cost.Mutations)*time.Second
	return cost, nil
}

// FirstAdmissibleProjectionIntent preserves due-order priority while allowing
// a later, cheaper module to proceed when an earlier effect profile cannot fit
// the remaining per-resource budget or runtime window.
func FirstAdmissibleProjectionIntent(intents []ProjectionIntentRecord, budget *ProjectionBudget, available time.Duration) (ProjectionIntentRecord, ProjectionEffectCost, bool, error) {
	for _, intent := range intents {
		cost, err := EstimateProjectionEffectCost(intent)
		if err != nil {
			return ProjectionIntentRecord{}, ProjectionEffectCost{}, false, err
		}
		if budget.AdmitEffect(cost, available) {
			return intent, cost, true, nil
		}
	}
	return ProjectionIntentRecord{}, ProjectionEffectCost{}, false, nil
}
