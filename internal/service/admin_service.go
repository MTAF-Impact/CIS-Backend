package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/cis/cis-backend/internal/aiclient"
	"github.com/cis/cis-backend/internal/models"
	"github.com/cis/cis-backend/internal/pkg/apperr"
	"github.com/cis/cis-backend/internal/repository"
)

// maxSampleContentCount mirrors the AI service's own cap on one synthetic
// ingestion request.
const maxSampleContentCount = 50

// AdminService serves the F4 MVP utilities and the operational jobs that drive
// the AI pipeline.
//
// Four of its methods exist for one structural reason: the frontend can only
// reach this backend, and the capabilities they expose — generating a claim,
// generating content, clustering, rescoring — write AI-owned tables and so can
// only be performed by the AI service. An AI endpoint with no backend caller is,
// from the product's point of view, an endpoint that does not exist.
type AdminService struct {
	ai       *aiclient.Client
	settings *SettingService
	policies *repository.PolicyRepository
	claims   *repository.ClaimRepository
}

// NewAdminService constructs an AdminService.
func NewAdminService(
	ai *aiclient.Client,
	settings *SettingService,
	policies *repository.PolicyRepository,
	claims *repository.ClaimRepository,
) *AdminService {
	return &AdminService{ai: ai, settings: settings, policies: policies, claims: claims}
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
//
// The AI service documents this as 30-60s of sequential LLM calls, so it runs
// on AI_SERVICE_LONG_TIMEOUT rather than the short hand-off timeout.
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
		return nil, aiUnavailable("generate a claim", err)
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

// GenerateSampleContentInput is the validated "Generate sample data" request.
type GenerateSampleContentInput struct {
	Count       int
	TopicHint   *string
	AutoCluster *bool
}

// SampleContentResult reports what a sample-data run produced.
type SampleContentResult struct {
	GeneratedCount int `json:"generated_count"`
	FailedCount    int `json:"failed_count"`
	// The three counts below are null when auto_cluster was false: nothing was
	// clustered, which is different from clustering that produced nothing.
	ClaimsCreated         *int      `json:"claims_created"`
	ClaimsUpdated         *int      `json:"claims_updated"`
	ContentItemsClustered *int      `json:"content_items_clustered"`
	LastFetchedAt         time.Time `json:"last_fetched_at"`
	Message               string    `json:"message"`
}

// GenerateSampleContent populates the databank with fabricated content
// (Flow 6).
//
// Until a live crawler exists, the AI service's synthetic ingestion is the only
// way `content_items` — and therefore Existing claims — come into being at all.
// Without this proxy an operator has to curl the AI service directly, which the
// deployment's network boundary may not even allow.
//
// Only the synthetic generator is proxied. The AI service's plain /ingest and
// /ingest/batch endpoints are for a machine crawler and should be called
// directly by it, never routed through a human-facing backend.
func (s *AdminService) GenerateSampleContent(ctx context.Context, in GenerateSampleContentInput, triggeredBy *uuid.UUID) (*SampleContentResult, error) {
	if !s.ai.Enabled() {
		return nil, apperr.Unavailable(
			"generating sample content requires the AI service, because content_items and claims are owned " +
				"and written exclusively by it. Set AI_SERVICE_URL to enable this button.")
	}
	if in.Count < 0 || in.Count > maxSampleContentCount {
		return nil, apperr.Unprocessable("count must be between 1 and %d", maxSampleContentCount)
	}

	res, err := s.ai.GenerateSampleContent(ctx, aiclient.GenerateContentRequest{
		Count:       in.Count,
		TopicHint:   in.TopicHint,
		AutoCluster: in.AutoCluster,
	})
	if err != nil {
		return nil, aiUnavailable("generate sample content", err)
	}

	// New content means new claims, so the S1 "last fetched" label must move for
	// the same reason US33 requires it to after the claim generator runs.
	lastFetched, err := s.settings.TouchClaimsLastFetchedAt(ctx, time.Now().UTC(), triggeredBy)
	if err != nil {
		return nil, err
	}

	out := &SampleContentResult{
		GeneratedCount:        len(res.Generated),
		FailedCount:           len(res.Failed),
		ClaimsCreated:         res.ClaimsCreated,
		ClaimsUpdated:         res.ClaimsUpdated,
		ContentItemsClustered: res.ContentItemsClustered,
		LastFetchedAt:         lastFetched,
		Message:               fmt.Sprintf("generated %d content items", len(res.Generated)),
	}
	return out, nil
}

// ClusterResult reports the outcome of a forced clustering pass.
type ClusterResult struct {
	ClaimsCreated         int `json:"claims_created"`
	ClaimsUpdated         int `json:"claims_updated"`
	ContentItemsClustered int `json:"content_items_clustered"`
}

// ClusterNow forces a clustering pass over unclustered content.
//
// Normally unnecessary — ingestion triggers clustering on its own — but useful
// after an ingest whose background pass has not finished, or to force one
// without waiting.
func (s *AdminService) ClusterNow(ctx context.Context) (*ClusterResult, error) {
	if !s.ai.Enabled() {
		return nil, apperr.Unavailable(
			"clustering requires the AI service. Set AI_SERVICE_URL to enable this action.")
	}

	res, err := s.ai.ClusterNow(ctx)
	if err != nil {
		return nil, aiUnavailable("run clustering", err)
	}
	return &ClusterResult{
		ClaimsCreated:         res.ClaimsCreated,
		ClaimsUpdated:         res.ClaimsUpdated,
		ContentItemsClustered: res.ContentItemsClustered,
	}, nil
}

// RescoreResult reports how many claims the AI service re-evaluated.
type RescoreResult struct {
	ClaimsRescored int `json:"claims_rescored"`
}

// Rescore asks the AI service to re-evaluate every existing claim (Flow 5).
//
// This is what keeps the F3 trend chart from being a straight line. Scores
// change with wall-clock time even when nothing is ingested: NPR drifts as
// opposing posts age out of the rolling window, which moves the discount factor
// and therefore final_claim_score. Nothing in either service recomputes that on
// a schedule, so the backend's snapshot job calls this first — see
// scheduler.runScoreSnapshot — and this endpoint exposes the same trigger
// manually.
func (s *AdminService) Rescore(ctx context.Context) (*RescoreResult, error) {
	if !s.ai.Enabled() {
		return nil, apperr.Unavailable(
			"rescoring requires the AI service, which owns every score column. " +
				"Set AI_SERVICE_URL to enable this action.")
	}

	res, err := s.ai.Rescore(ctx)
	if err != nil {
		return nil, aiUnavailable("rescore claims", err)
	}
	return &RescoreResult{ClaimsRescored: res.ClaimsRescored}, nil
}

// RescoreIfEnabled runs a rescore and swallows a "not configured" result.
//
// Used by the snapshot cron, which must still capture whatever scores exist
// when no AI service is configured.
func (s *AdminService) RescoreIfEnabled(ctx context.Context) (int, error) {
	if !s.ai.Enabled() {
		return 0, nil
	}
	res, err := s.ai.Rescore(ctx)
	if err != nil {
		return 0, err
	}
	return res.ClaimsRescored, nil
}

// ReconcileResult reports what a reconciliation swept up.
type ReconcileResult struct {
	DryRun                 bool   `json:"dry_run"`
	OrphanedReviews        int64  `json:"orphaned_reviews"`
	OrphanedAlerts         int64  `json:"orphaned_alerts"`
	OrphanedScoreSnapshots int64  `json:"orphaned_score_snapshots"`
	PoliciesUnlinked       int64  `json:"policies_unlinked"`
	ClaimsInDatabase       int64  `json:"claims_in_database"`
	AIPoliciesInDatabase   int64  `json:"ai_policies_in_database"`
	Message                string `json:"message"`
}

// Reconcile clears backend rows whose AI-side counterpart no longer exists.
//
// Every backend reference into an AI table is a soft one — cis_claim_reviews,
// cis_claim_alerts and cis_claim_score_snapshots point at claims.id, and
// cis_policies.ai_policy_id points at policies.id, all without a foreign key,
// because the backend must never constrain a table it does not own. So when the
// AI team runs a demo reseed or a schema reset (both of which correctly leave
// cis_* alone), nothing cascades: the watchlist keeps rows for claims that no
// longer exist, and a policy keeps a completed badge while its claim lists are
// silently empty. Nothing errors; the UI just shows wrong things.
//
// A policy whose AI counterpart vanished is not merely unlinked but re-queued,
// since the correlations it lost can be rebuilt by running matchmaking again.
//
// # The empty-database guard
//
// If the AI tables are present but empty, every backend reference looks orphaned
// and a full sweep would erase the entire human layer — every review decision
// and every watchlist entry. That is indistinguishable, from here, from being
// pointed at the wrong database. So the sweep refuses to run when the claims
// table is empty and there is anything to delete, unless the caller passes
// force.
func (s *AdminService) Reconcile(ctx context.Context, dryRun, force bool) (*ReconcileResult, error) {
	claimCount, err := s.claims.CountAllClaims(ctx)
	if err != nil {
		return nil, apperr.Internal("could not count claims").Wrap(err)
	}
	policyCount, err := s.policies.CountAIPolicies(ctx)
	if err != nil {
		return nil, apperr.Internal("could not count AI policies").Wrap(err)
	}

	counts, err := s.claims.CountOrphanedOverlays(ctx)
	if err != nil {
		return nil, apperr.Internal("could not find orphaned claim references").Wrap(err)
	}
	danglingPolicies, err := s.policies.CountDanglingAIPolicyLinks(ctx)
	if err != nil {
		return nil, apperr.Internal("could not find orphaned policy links").Wrap(err)
	}

	result := &ReconcileResult{
		DryRun:                 dryRun,
		OrphanedReviews:        counts.Reviews,
		OrphanedAlerts:         counts.Alerts,
		OrphanedScoreSnapshots: counts.Snapshots,
		PoliciesUnlinked:       danglingPolicies,
		ClaimsInDatabase:       claimCount,
		AIPoliciesInDatabase:   policyCount,
	}

	total := counts.Reviews + counts.Alerts + counts.Snapshots + danglingPolicies
	if total == 0 {
		result.Message = "nothing to reconcile"
		return result, nil
	}
	if claimCount == 0 && !force {
		return nil, apperr.Conflict(
			"refusing to reconcile: the AI service's claims table is empty, so every backend review, "+
				"watchlist entry and snapshot looks orphaned (%d rows). This usually means the backend is "+
				"pointed at the wrong database. Pass force=true only if the AI data really was wiped "+
				"deliberately.", total)
	}

	if dryRun {
		result.Message = fmt.Sprintf("%d rows would be reconciled", total)
		return result, nil
	}

	if err := s.claims.DeleteOrphanedOverlays(ctx); err != nil {
		return nil, apperr.Internal("could not clear orphaned claim references").Wrap(err)
	}

	// A policy whose AI record vanished can be matched again, so it goes back to
	// pending rather than merely losing its link — unless there is no AI service
	// to match it, in which case "skipped" is the honest badge.
	requeueStatus := models.ProcessingSkipped
	if s.ai.Enabled() {
		requeueStatus = models.ProcessingPending
	}
	if err := s.policies.ClearDanglingAIPolicyLinks(ctx, requeueStatus); err != nil {
		return nil, apperr.Internal("could not clear orphaned policy links").Wrap(err)
	}

	result.Message = fmt.Sprintf("%d rows reconciled", total)
	return result, nil
}

// aiUnavailable renders an AI-service failure as a 503 the operator can act on.
func aiUnavailable(action string, err error) error {
	if errors.Is(err, aiclient.ErrNotConfigured) {
		return apperr.Unavailable("the AI service is not configured")
	}
	return apperr.Unavailable("the AI service could not %s: %s", action, err.Error())
}
