package service

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/cis/cis-backend/internal/models"
	"github.com/cis/cis-backend/internal/pkg/apperr"
	"github.com/cis/cis-backend/internal/repository"
)

// evaluateThresholdCrossings compares every watched claim's current Over/Under
// status against the status recorded at the previous evaluation and stamps the
// ones that just flipped.
//
// It is a free function rather than a method because two callers need it and
// they sit on opposite sides of the graph: the hourly snapshot job, which is
// AlertService's, and the harm confirmation on the claim detail page, since
// confirming harm can move a claim's score across the alert threshold and
// that has to re-evaluate immediately. Making it a method on either service
// would mean giving that service a dependency on the other purely to reach
// one loop.
//
// Passing no claimIDs evaluates the whole watchlist.
//
// A claim whose stored status is empty has never been evaluated — it was
// bell-icon'd since the last pass — so its current status is recorded as the
// baseline without notifying. A crossing only fires for a genuine transition,
// never for a claim that is already steady above or below threshold, and a
// first sighting is not a transition.
func evaluateThresholdCrossings(
	ctx context.Context,
	alerts *repository.AlertRepository,
	threshold float64,
	claimIDs ...uuid.UUID,
) (int, error) {
	states, err := alerts.ListThresholdStates(ctx, claimIDs)
	if err != nil {
		return 0, apperr.Internal("could not load watchlist threshold state").Wrap(err)
	}

	now := time.Now().UTC()
	crossings := 0

	for _, state := range states {
		current := thresholdStatus(state.FinalClaimScore, threshold)
		if current == state.LastStatus {
			continue
		}

		direction := ""
		if state.LastStatus != models.ThresholdStatusUnknown {
			direction = models.CrossingDirectionDown
			if current == models.ThresholdStatusOver {
				direction = models.CrossingDirectionUp
			}
			crossings++
		}

		if err := alerts.RecordThresholdStatus(ctx, state.ClaimID, current, direction, now); err != nil {
			return crossings, apperr.Internal("could not record a threshold crossing").Wrap(err)
		}
	}
	return crossings, nil
}

// thresholdStatus resolves a score against the global alert threshold.
//
// An unscored claim is Under. The threshold decides escalation, and escalating
// on a missing number is the one direction that cannot be defended.
func thresholdStatus(score *float64, threshold float64) string {
	if score != nil && *score >= threshold {
		return models.ThresholdStatusOver
	}
	return models.ThresholdStatusUnder
}
