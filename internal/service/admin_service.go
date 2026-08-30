package service

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/cis/cis-backend/internal/aiclient"
	"github.com/cis/cis-backend/internal/models"
	"github.com/cis/cis-backend/internal/pkg/apperr"
)

// AdminService serves the F4 MVP utilities.
type AdminService struct {
	ai       *aiclient.Client
	settings *SettingService
}

// NewAdminService constructs an AdminService.
func NewAdminService(ai *aiclient.Client, settings *SettingService) *AdminService {
	return &AdminService{ai: ai, settings: settings}
}

// GenerateClaimResult reports the outcome of the F4 test-data button.
type GenerateClaimResult struct {
	ClaimID        *string   `json:"claim_id"`
	ClaimStatement string    `json:"claim_statement"`
	TopicID        *string   `json:"topic_id"`
	LastFetchedAt  time.Time `json:"last_fetched_at"`
	Message        string    `json:"message"`
}

// GenerateGenericClaim triggers the "Generate Generic Claim" button (US33).
//
// The backend cannot create the claim itself: `claims` is owned by the AI
// service and this backend never writes AI-owned tables. So the request is
// proxied to the AI service, which inserts a fully-populated claim (score
// breakdown, statements, top accounts, cached debunk draft) and returns its id.
func (s *AdminService) GenerateGenericClaim(ctx context.Context, topicID *string, triggeredBy *uuid.UUID) (*GenerateClaimResult, error) {
	if !s.ai.Enabled() {
		return nil, apperr.Unavailable(
			"the Generate Generic Claim utility requires the AI service, because claims are owned and written " +
				"exclusively by it. Set AI_SERVICE_URL to enable this button.")
	}

	res, err := s.ai.GenerateGenericClaim(ctx, aiclient.GenerateClaimRequest{
		ClaimType: models.ClaimTypeExisting,
		TopicID:   topicID,
	})
	if err != nil {
		if errors.Is(err, aiclient.ErrNotConfigured) {
			return nil, apperr.Unavailable("the AI service is not configured")
		}
		return nil, apperr.Unavailable("the AI service could not generate a claim: %s", err.Error())
	}

	// US33: the S1 "last fetched" label must move to the moment the button was
	// clicked.
	now := time.Now().UTC()
	lastFetched, err := s.settings.TouchClaimsLastFetchedAt(ctx, now, triggeredBy)
	if err != nil {
		return nil, err
	}

	out := &GenerateClaimResult{
		ClaimStatement: res.ClaimStatement,
		LastFetchedAt:  lastFetched,
		Message:        res.Message,
	}
	if res.ClaimID != nil {
		id := res.ClaimID.String()
		out.ClaimID = &id
	}
	if res.TopicID != nil {
		id := res.TopicID.String()
		out.TopicID = &id
	}
	if out.Message == "" {
		out.Message = "generic claim generated"
	}
	return out, nil
}
