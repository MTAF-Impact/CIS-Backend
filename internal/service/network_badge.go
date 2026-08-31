package service

import (
	"context"

	"github.com/google/uuid"

	"github.com/cis/cis-backend/internal/dto"
	"github.com/cis/cis-backend/internal/pkg/apperr"
	"github.com/cis/cis-backend/internal/repository"
)

// networkDetailPath is the F5 network detail route the US61 indicator links to.
// Built here rather than in the frontend so the badge is self-contained: the
// same object is rendered by three different card paths and a detail page.
const networkDetailPath = "/api/v1/networks/"

// networkBadges resolves the US61 cross-link for a page of claims.
//
// Only Existing/Generic claims are looked up. PRD 10.3 puts detection over
// Non-Existing/Synthetic claims out of scope — a predicted claim has no real
// posts, so nothing can be amplifying it — which makes a link for one a
// contradiction rather than a case to render.
//
// A pipeline that was never deployed is not an error here. BadgesForClaims
// already degrades an absent schema to an empty map, matching US61's "no empty
// state": no qualifying network and no detector at all look the same from F1,
// which is correct, because in both cases there is nothing to show.
func networkBadges(
	ctx context.Context, networks *repository.NetworkRepository, claimIDs []uuid.UUID,
) (map[uuid.UUID]repository.NetworkBadge, error) {
	if networks == nil || len(claimIDs) == 0 {
		return nil, nil
	}
	badges, err := networks.BadgesForClaims(ctx, claimIDs)
	if err != nil {
		return nil, apperr.Internal("could not resolve coordinated networks").Wrap(err)
	}
	return badges, nil
}

// toNetworkBadge shapes one resolved badge for the API.
//
// Returns nil when the claim has no qualifying network, so callers can assign
// the result straight onto the omitempty field: US61 says show nothing, and an
// explicit null is not nothing.
func toNetworkBadge(badges map[uuid.UUID]repository.NetworkBadge, claimID uuid.UUID) *dto.ClaimNetworkBadge {
	badge, ok := badges[claimID]
	if !ok {
		return nil
	}
	return &dto.ClaimNetworkBadge{
		NetworkID:         badge.NetworkID.String(),
		Label:             badge.Label,
		CoordinationScore: badge.CoordinationScore,
		ConfidenceBand:    badge.ConfidenceBand,
		ReviewStatus:      badge.ReviewStatus,
		AccountCount:      badge.AccountCount,
		OtherCount:        badge.OtherCount,
		DetailURL:         networkDetailPath + badge.NetworkID.String(),
	}
}
