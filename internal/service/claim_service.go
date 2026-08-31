package service

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/cis/cis-backend/internal/aiclient"
	"github.com/cis/cis-backend/internal/dto"
	"github.com/cis/cis-backend/internal/models"
	"github.com/cis/cis-backend/internal/pkg/apperr"
	"github.com/cis/cis-backend/internal/repository"
	"github.com/cis/cis-backend/internal/scoring"
)

// TopAccountLimit is the size of the US12 Top 5 Accounts panel.
const TopAccountLimit = 5

// SectionSize is the number of claims each F1 section shows before "See all"
// (US7, US16).
const SectionSize = 10

// ClaimService assembles the F1 Claim Repository Bank payloads.
//
// It holds an AI client for exactly one reason: harm confirmation (Flow 4)
// writes claims.harm_* columns, which this backend never writes itself. Every
// other field on this page is a plain database read.
type ClaimService struct {
	claims    *repository.ClaimRepository
	alerts    *repository.AlertRepository
	policies  *repository.PolicyRepository
	snapshots *repository.SnapshotRepository
	// networks resolves the US61 "Coordinated network detected" indicator. It
	// is the one F5 dependency F1 has, and it is read-only.
	networks *repository.NetworkRepository
	settings *SettingService
	ai       *aiclient.Client
}

// NewClaimService constructs a ClaimService.
func NewClaimService(
	claims *repository.ClaimRepository,
	alerts *repository.AlertRepository,
	policies *repository.PolicyRepository,
	snapshots *repository.SnapshotRepository,
	networks *repository.NetworkRepository,
	settings *SettingService,
	ai *aiclient.Client,
) *ClaimService {
	return &ClaimService{
		claims:    claims,
		alerts:    alerts,
		policies:  policies,
		snapshots: snapshots,
		networks:  networks,
		settings:  settings,
		ai:        ai,
	}
}

// ListClaimsQuery is the normalized input for the "See all" list (US8, US17).
type ListClaimsQuery struct {
	ClaimType string
	Status    string
	TopicIDs  []uuid.UUID
	Search    string
	SortBy    string
	Page      int
	Limit     int
}

// Repository builds the whole F1 page: both sections plus the last-fetched
// timestamp.
//
// Both sections are always returned. Per US1 the status tab filters claims
// within each section and never hides a section outright.
func (s *ClaimService) Repository(ctx context.Context, status string, topicIDs []uuid.UUID, search string) (*dto.RepositoryResponse, error) {
	existing, err := s.buildSection(ctx, models.ClaimTypeExisting, status, topicIDs, search)
	if err != nil {
		return nil, err
	}
	nonExisting, err := s.buildSection(ctx, models.ClaimTypeNonExisting, status, topicIDs, search)
	if err != nil {
		return nil, err
	}

	lastFetched, err := s.settings.ClaimsLastFetchedAt(ctx)
	if err != nil {
		return nil, err
	}

	topics := make([]string, 0, len(topicIDs))
	for _, id := range topicIDs {
		topics = append(topics, id.String())
	}
	if status == "" {
		status = "all"
	}

	return &dto.RepositoryResponse{
		LastFetchedAt: lastFetched,
		AppliedStatus: status,
		AppliedTopics: topics,
		Existing:      *existing,
		NonExisting:   *nonExisting,
	}, nil
}

func (s *ClaimService) buildSection(ctx context.Context, claimType, status string, topicIDs []uuid.UUID, search string) (*dto.ClaimSection, error) {
	// S1 ranks by FinalClaimScore (US7); S2 by newest predicted claim (US16).
	sortBy := repository.SortByScore
	section, label, sortedBy := "S1", "Existing Claim (Generic Claim)", "final_claim_score DESC"
	if claimType == models.ClaimTypeNonExisting {
		sortBy = repository.SortByCreatedAt
		section, label, sortedBy = "S2", "Non-Existing Claim (Synthetic Claim)", "created_at DESC"
	}

	filter := repository.ClaimFilter{
		ClaimType:    claimType,
		ReviewStatus: status,
		TopicIDs:     topicIDs,
		Search:       search,
		SortBy:       sortBy,
		Limit:        SectionSize,
	}

	rows, err := s.claims.ListClaims(ctx, filter)
	if err != nil {
		return nil, apperr.Internal("could not load claims").Wrap(err)
	}
	total, err := s.claims.CountClaims(ctx, filter)
	if err != nil {
		return nil, apperr.Internal("could not count claims").Wrap(err)
	}

	cards, err := s.toCards(ctx, rows)
	if err != nil {
		return nil, err
	}

	return &dto.ClaimSection{
		Section:     section,
		Label:       label,
		ClaimType:   claimType,
		SortedBy:    sortedBy,
		TotalInPool: total,
		Claims:      cards,
	}, nil
}

// List returns a paginated claim list for the "See all" pages.
func (s *ClaimService) List(ctx context.Context, q ListClaimsQuery) ([]dto.ClaimCard, int64, dto.PageParams, error) {
	page := dto.NormalizePage(q.Page, q.Limit)

	sortBy := q.SortBy
	if sortBy == "" {
		// Default per section: Existing claims rank by score, Synthetic by
		// recency.
		sortBy = repository.SortByScore
		if q.ClaimType == models.ClaimTypeNonExisting {
			sortBy = repository.SortByCreatedAt
		}
	}

	filter := repository.ClaimFilter{
		ClaimType:    q.ClaimType,
		ReviewStatus: q.Status,
		TopicIDs:     q.TopicIDs,
		Search:       q.Search,
		SortBy:       sortBy,
		Limit:        page.Limit,
		Offset:       page.Offset(),
	}

	rows, err := s.claims.ListClaims(ctx, filter)
	if err != nil {
		return nil, 0, page, apperr.Internal("could not load claims").Wrap(err)
	}
	total, err := s.claims.CountClaims(ctx, filter)
	if err != nil {
		return nil, 0, page, apperr.Internal("could not count claims").Wrap(err)
	}

	cards, err := s.toCards(ctx, rows)
	if err != nil {
		return nil, 0, page, err
	}
	return cards, total, page, nil
}

// toCards converts claim rows into card DTOs, batching the statement counts and
// alert lookups so a page of cards costs a constant number of queries.
func (s *ClaimService) toCards(ctx context.Context, rows []repository.ClaimRow) ([]dto.ClaimCard, error) {
	cards := make([]dto.ClaimCard, 0, len(rows))
	if len(rows) == 0 {
		return cards, nil
	}

	// Only Existing claims carry counts, alert state, and a score (US18).
	existingIDs := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		if models.NormalizeClaimType(row.ClaimType) == models.ClaimTypeExisting {
			existingIDs = append(existingIDs, row.ID)
		}
	}

	counts, err := s.claims.CountStatementsByClaim(ctx, existingIDs)
	if err != nil {
		return nil, apperr.Internal("could not count claim statements").Wrap(err)
	}
	alerted, err := s.alerts.AlertedClaimIDs(ctx, existingIDs)
	if err != nil {
		return nil, apperr.Internal("could not resolve alert state").Wrap(err)
	}
	badges, err := networkBadges(ctx, s.networks, existingIDs)
	if err != nil {
		return nil, err
	}

	for _, row := range rows {
		cards = append(cards, buildClaimCard(row, counts, alerted, badges))
	}
	return cards, nil
}

// buildClaimCard converts one claim row into a card.
//
// Shared by F1 and by F2's detail page (US39), which reuses the identical card
// component and must therefore receive an identical payload.
func buildClaimCard(
	row repository.ClaimRow,
	counts map[uuid.UUID]repository.StanceCount,
	alerted map[uuid.UUID]bool,
	badges map[uuid.UUID]repository.NetworkBadge,
) dto.ClaimCard {
	card := dto.ClaimCard{
		ID:             row.ID.String(),
		ClaimType:      models.NormalizeClaimType(row.ClaimType),
		ClaimStatement: row.ClaimStatement,
		ReviewStatus:   row.ReviewStatus,
		CreatedAt:      row.CreatedAt,
	}
	if row.TopicName != nil {
		card.Topic = &dto.TopicRef{ID: row.TopicID.String(), Name: *row.TopicName}
	}

	// Synthetic claims carry no score, dates, or statement counts (US18).
	if card.ClaimType != models.ClaimTypeExisting {
		return card
	}

	firstCaught := row.FirstCaughtAt
	isDormant := row.IsDormant
	onAlert := alerted[row.ID]
	count := counts[row.ID]

	card.FinalClaimScore = scoring.ClampPtr(row.FinalClaimScore)
	card.FirstCaughtAt = &firstCaught
	card.PositiveStatementCount = &count.Positive
	card.NegativeStatementCount = &count.Negative
	card.IsDormant = &isDormant
	card.IsOnAlert = &onAlert
	// US61's triage icon. nil when nothing qualifies, so the field is omitted
	// rather than sent as null — the PRD is explicit that there is no empty
	// state for this indicator.
	card.CoordinatedNetwork = toNetworkBadge(badges, row.ID)
	return card
}

// Detail builds the claim detail page payload (US12 for Existing claims, US20
// for Synthetic ones).
func (s *ClaimService) Detail(ctx context.Context, id uuid.UUID) (*dto.ClaimDetail, error) {
	row, err := s.claims.FindClaimByID(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, apperr.NotFound("claim not found")
		}
		return nil, apperr.Internal("could not load claim").Wrap(err)
	}

	claimType := models.NormalizeClaimType(row.ClaimType)

	detail := &dto.ClaimDetail{
		ID:             row.ID.String(),
		ClaimType:      claimType,
		ClaimStatement: row.ClaimStatement,
		ReviewStatus:   row.ReviewStatus,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
		Activity:       buildActivity(claimType, row.AIClaim),
	}
	if row.TopicName != nil {
		detail.Topic = &dto.TopicRef{ID: row.TopicID.String(), Name: *row.TopicName}
	}
	if row.ReviewedAt != nil {
		review := &dto.ClaimReview{Notes: row.ReviewNotes, ReviewedAt: row.ReviewedAt}
		if row.ReviewedBy != nil {
			id := row.ReviewedBy.String()
			review.ReviewedBy = &id
		}
		detail.Review = review
	}

	policies, err := s.correlatedPolicies(ctx, row)
	if err != nil {
		return nil, err
	}
	detail.Policies = policies

	if claimType != models.ClaimTypeExisting {
		// Synthetic claims are never scored, never watched, and carry no
		// statement lists (US18, US20, US26).
		return detail, nil
	}

	firstCaught := row.FirstCaughtAt
	detail.FirstCaughtAt = &firstCaught
	detail.ScoreBreakdown = buildBreakdown(row)

	counts, err := s.claims.CountStatementsByClaim(ctx, []uuid.UUID{row.ID})
	if err != nil {
		return nil, apperr.Internal("could not count claim statements").Wrap(err)
	}
	count := counts[row.ID]
	detail.PositiveStatementCount = &count.Positive
	detail.NegativeStatementCount = &count.Negative

	accounts, err := s.TopAccounts(ctx, row.ID, TopAccountLimit)
	if err != nil {
		return nil, err
	}
	detail.TopAccounts = accounts

	alerted, err := s.alerts.AlertedClaimIDs(ctx, []uuid.UUID{row.ID})
	if err != nil {
		return nil, apperr.Internal("could not resolve alert state").Wrap(err)
	}
	onAlert := alerted[row.ID]
	detail.IsOnAlert = &onAlert

	// US61. This is the point of F5 in daily use: it is what decides whether
	// the team publicly rebuts this claim or refers it to the platform
	// instead, so it belongs on the page where that decision is made.
	badges, err := networkBadges(ctx, s.networks, []uuid.UUID{row.ID})
	if err != nil {
		return nil, err
	}
	detail.CoordinatedNetwork = toNetworkBadge(badges, row.ID)

	return detail, nil
}

// buildActivity wraps the cached AI draft. US12/US20 require this to be served
// from cache — the backend must never trigger a new generation on view.
//
// The AI service writes the Debunk twice: once flat, in activity_content, and
// once split into the Truth Sandwich's three blocks. Both are returned, because
// the flat version is what an operator copies and the split version is what the
// detail page labels and lays out.
func buildActivity(claimType string, claim models.AIClaim) dto.ActivityContent {
	// Existing claims get corrective Debunk content; Synthetic claims get
	// pre-emptive Prebunk content.
	activityType := "debunk"
	if claimType == models.ClaimTypeNonExisting {
		activityType = "prebunk"
	}

	activity := dto.ActivityContent{
		Type:        activityType,
		Content:     claim.ActivityContent,
		GeneratedAt: claim.ActivityGeneratedAt,
		Available:   claim.ActivityContent != nil && *claim.ActivityContent != "",
	}

	// Omitted rather than sent as three nulls, so the frontend can branch on
	// presence instead of inspecting each block.
	if claim.DebunkCoreFact != nil || claim.DebunkNuancedFlag != nil || claim.DebunkReiteratedFact != nil {
		activity.Debunk = &dto.DebunkBlocks{
			CoreFact:       claim.DebunkCoreFact,
			NuancedFlag:    claim.DebunkNuancedFlag,
			ReiteratedFact: claim.DebunkReiteratedFact,
		}
	}
	return activity
}

// buildBreakdown assembles the US23 Score Transparency payload.
//
// Every component is returned together with FinalClaimScore so the collapsed
// number is never presented without its inputs. Values are clamped defensively
// per PRD 6.3 / 6.4.4.
func buildBreakdown(row *repository.ClaimRow) *dto.ScoreBreakdown {
	breakdown := &dto.ScoreBreakdown{
		Reach:                      scoring.ClampPtr(row.ReachScore),
		Velocity:                   scoring.ClampPtr(row.VelocityScore),
		Falseness:                  scoring.ClampPtr(row.FalsenessScore),
		Harm:                       scoring.ClampPtr(row.HarmScore),
		EmotionalIntensity:         scoring.ClampPtr(row.EmotionalIntensityScore),
		EmotionalIntensityOpposing: scoring.ClampPtr(row.EmotionalIntensityOpposing),
		HarmBreakdown: dto.HarmBreakdown{
			PublicSafety:       scoring.ClampPtr(row.HarmPublicSafety),
			InstitutionalTrust: scoring.ClampPtr(row.HarmInstitutionalTrust),
			Economic:           scoring.ClampPtr(row.HarmEconomic),
			PolicyDisruption:   scoring.ClampPtr(row.HarmPolicyDisruption),
			HumanConfirmed:     row.HarmHumanConfirmed,
			Weights:            scoring.PublishedHarmWeights(),
		},
		ClaimScore:      scoring.ClampPtr(row.ClaimScore),
		NPR:             scoring.ClampRatio(row.NPR),
		DiscountFactor:  scoring.ClampDiscount(row.DiscountFactor),
		FinalClaimScore: scoring.ClampPtr(row.FinalClaimScore),
		IsDormant:       row.IsDormant,
		Weights:         scoring.PublishedWeights(),
	}

	// US25 / PRD 6.4.7: a dormant claim has no volume to measure pushback
	// against, so NPR and the discount must be shown as not-applicable rather
	// than as numbers that would imply its priority was reduced.
	if row.IsDormant {
		breakdown.NPR = nil
		breakdown.DiscountFactor = nil
		breakdown.Note = scoring.DormancyNote
	}
	return breakdown
}

// correlatedPolicies resolves the policies linked to a claim, preferring this
// backend's F2 record when one shadows the AI policy id.
func (s *ClaimService) correlatedPolicies(ctx context.Context, row *repository.ClaimRow) ([]dto.PolicyRef, error) {
	aiPolicyIDs, err := s.claims.ListPolicyIDsForClaim(ctx, row.ID, row.PolicyID)
	if err != nil {
		return nil, apperr.Internal("could not resolve correlated policies").Wrap(err)
	}
	refs := make([]dto.PolicyRef, 0, len(aiPolicyIDs))
	if len(aiPolicyIDs) == 0 {
		return refs, nil
	}

	cisPolicies, err := s.policies.FindByAIPolicyIDs(ctx, aiPolicyIDs)
	if err != nil {
		return nil, apperr.Internal("could not load policy records").Wrap(err)
	}
	byAIID := make(map[uuid.UUID]models.CISPolicy, len(cisPolicies))
	for _, p := range cisPolicies {
		if p.AIPolicyID != nil {
			byAIID[*p.AIPolicyID] = p
		}
	}

	aiPolicies, err := s.policies.FindAIPoliciesByIDs(ctx, aiPolicyIDs)
	if err != nil {
		return nil, apperr.Internal("could not load AI policy records").Wrap(err)
	}
	aiByID := make(map[uuid.UUID]models.AIPolicy, len(aiPolicies))
	for _, p := range aiPolicies {
		aiByID[p.ID] = p
	}

	for _, aiID := range aiPolicyIDs {
		if p, ok := byAIID[aiID]; ok {
			aiIDStr := aiID.String()
			status := p.Status
			rolledOut := p.RolledOutDate
			refs = append(refs, dto.PolicyRef{
				ID:            p.ID.String(),
				Name:          p.Name,
				Source:        "cis",
				AIPolicyID:    &aiIDStr,
				Status:        &status,
				RolledOutDate: &rolledOut,
				HasDocument:   p.FilePath != "",
			})
			continue
		}
		// A policy the AI service created directly, with no F2 record behind it.
		if p, ok := aiByID[aiID]; ok {
			aiIDStr := aiID.String()
			refs = append(refs, dto.PolicyRef{
				ID:          p.ID.String(),
				Name:        p.Title,
				Source:      "ai",
				AIPolicyID:  &aiIDStr,
				HasDocument: false,
			})
		}
	}
	return refs, nil
}

// Statements returns a page of a claim's source posts.
//
// Positive maps to supporting stance and Negative to opposing, matching the NPR
// definition in PRD 6.4.2 so the counts always agree with the score.
func (s *ClaimService) Statements(ctx context.Context, claimID uuid.UUID, kind string, page, limit int) ([]dto.Statement, int64, dto.PageParams, error) {
	window := dto.NormalizePage(page, limit)

	var stance string
	switch kind {
	case "", "all":
		stance = ""
	case "positive", models.StanceSupporting:
		stance = models.StanceSupporting
	case "negative", models.StanceOpposing:
		stance = models.StanceOpposing
	case "neutral":
		stance = models.StanceNeutral
	default:
		return nil, 0, window, apperr.BadRequest("stance must be one of: positive, negative, neutral, all")
	}

	items, total, err := s.claims.ListStatements(ctx, claimID, stance, window.Limit, window.Offset())
	if err != nil {
		return nil, 0, window, apperr.Internal("could not load statements").Wrap(err)
	}

	out := make([]dto.Statement, 0, len(items))
	for _, item := range items {
		out = append(out, dto.Statement{
			ID:                    item.ID.String(),
			Text:                  item.Text,
			Source:                item.Source,
			AuthorID:              item.AuthorID,
			Location:              item.Location,
			Stance:                item.Stance,
			OutrageScore:          item.OutrageScore,
			Impressions:           item.Impressions,
			PositiveReactionCount: item.PositiveReactionCount,
			NegativeReactionCount: item.NegativeReactionCount,
			CreatedAt:             item.CreatedAt,
		})
	}
	return out, total, window, nil
}

// TopAccounts returns the accounts driving a claim's spread (US12).
func (s *ClaimService) TopAccounts(ctx context.Context, claimID uuid.UUID, limit int) ([]dto.AccountRef, error) {
	if limit < 1 {
		limit = TopAccountLimit
	}
	rows, err := s.claims.ListTopAccounts(ctx, claimID, limit)
	if err != nil {
		return nil, apperr.Internal("could not load top accounts").Wrap(err)
	}

	out := make([]dto.AccountRef, 0, len(rows))
	for i, row := range rows {
		out = append(out, dto.AccountRef{
			Rank:             i + 1,
			AuthorID:         row.AuthorID,
			ContentCount:     row.ContentCount,
			TotalImpressions: row.TotalImpressions,
		})
	}
	return out, nil
}

// Policies returns a claim's correlated policies.
func (s *ClaimService) Policies(ctx context.Context, claimID uuid.UUID) ([]dto.PolicyRef, error) {
	row, err := s.claims.FindClaimByID(ctx, claimID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, apperr.NotFound("claim not found")
		}
		return nil, apperr.Internal("could not load claim").Wrap(err)
	}
	return s.correlatedPolicies(ctx, row)
}

// UpdateStatus records a reviewer's status decision (US10, US18).
//
// This writes the backend-owned cis_claim_reviews overlay; the AI service's
// claims.status is never modified, so a pipeline re-run cannot overwrite a
// human decision.
func (s *ClaimService) UpdateStatus(ctx context.Context, claimID uuid.UUID, req dto.UpdateClaimStatusRequest, reviewedBy *uuid.UUID) (*dto.ClaimStatusResponse, error) {
	if !models.IsValidReviewStatus(req.Status) {
		return nil, apperr.Unprocessable("status must be one of: unreviewed, active, inactive, action_taken")
	}

	exists, _, err := s.claims.ClaimExists(ctx, claimID)
	if err != nil {
		return nil, apperr.Internal("could not verify claim").Wrap(err)
	}
	if !exists {
		return nil, apperr.NotFound("claim not found")
	}

	review, err := s.claims.UpsertReview(ctx, claimID, req.Status, req.Notes, reviewedBy)
	if err != nil {
		return nil, apperr.Internal("could not save claim status").Wrap(err)
	}

	res := &dto.ClaimStatusResponse{
		ClaimID:      claimID.String(),
		ReviewStatus: review.Status,
		Notes:        review.Notes,
		ReviewedAt:   review.ReviewedAt,
	}
	if review.ReviewedBy != nil {
		id := review.ReviewedBy.String()
		res.ReviewedBy = &id
	}
	return res, nil
}

// ConfirmHarm records a reviewer's confirmation of a claim's Harm sub-scores
// (Flow 4, PRD 6.2.4).
//
// This is the one claim mutation the backend cannot perform itself: the four
// harm_* columns, harm_human_confirmed, and every score derived from them live
// on the AI-owned `claims` table. So the request is proxied to the AI service,
// which applies the overrides and recomputes harm_score -> claim_score ->
// final_claim_score, and the claim is then re-read from the database so the
// response is built from the same source as every other claim read.
//
// Synthetic claims are rejected before the call: they carry no scores at all
// (US18), so there is nothing to confirm.
func (s *ClaimService) ConfirmHarm(ctx context.Context, claimID uuid.UUID, req dto.ConfirmHarmRequest) (*dto.ClaimDetail, error) {
	if !s.ai.Enabled() {
		return nil, apperr.Unavailable(
			"confirming harm sub-scores requires the AI service, because the claims table is owned and " +
				"written exclusively by it. Set AI_SERVICE_URL to enable this action.")
	}

	exists, rawType, err := s.claims.ClaimExists(ctx, claimID)
	if err != nil {
		return nil, apperr.Internal("could not verify claim").Wrap(err)
	}
	if !exists {
		return nil, apperr.NotFound("claim not found")
	}
	if models.NormalizeClaimType(rawType) != models.ClaimTypeExisting {
		return nil, apperr.Unprocessable(
			"only Existing (Generic) claims carry harm sub-scores; " +
				"Non-Existing (Synthetic) claims are unscored predictions")
	}

	err = s.ai.ConfirmHarm(ctx, claimID, aiclient.HarmConfirmRequest{
		PublicSafety:       req.PublicSafety,
		InstitutionalTrust: req.InstitutionalTrust,
		Economic:           req.Economic,
		PolicyDisruption:   req.PolicyDisruption,
	})
	if err != nil {
		if errors.Is(err, aiclient.ErrNotConfigured) {
			return nil, apperr.Unavailable("the AI service is not configured")
		}
		return nil, apperr.Unavailable("the AI service could not record the harm confirmation: %s", err.Error())
	}

	return s.Detail(ctx, claimID)
}

// ScoreHistory returns a claim's FinalClaimScore over time, from the
// backend-owned snapshot table.
func (s *ClaimService) ScoreHistory(ctx context.Context, claimID uuid.UUID, granularity string, from, to *time.Time) (*dto.ScoreHistoryResponse, error) {
	trunc, err := repository.GranularityToTrunc(granularity)
	if err != nil {
		return nil, apperr.BadRequest("granularity must be one of: day, week, month, year")
	}

	points, err := s.snapshots.Series(ctx, repository.SeriesFilter{
		ClaimIDs: []uuid.UUID{claimID},
		Trunc:    trunc,
		From:     from,
		To:       to,
	})
	if err != nil {
		return nil, apperr.Internal("could not load score history").Wrap(err)
	}

	out := make([]dto.ScorePoint, 0, len(points))
	for _, p := range points {
		out = append(out, dto.ScorePoint{
			BucketStart:     p.BucketStart,
			FinalClaimScore: scoring.ClampPtr(p.FinalClaimScore),
			ClaimScore:      scoring.ClampPtr(p.ClaimScore),
			SampleCount:     p.SampleCount,
		})
	}

	return &dto.ScoreHistoryResponse{
		ClaimID:     claimID.String(),
		Granularity: trunc,
		Points:      out,
	}, nil
}

// TopicView is a topic filter chip annotated with claim counts.
type TopicView struct {
	ID                    string  `json:"id"`
	Name                  string  `json:"name"`
	Description           *string `json:"description"`
	ExistingClaimCount    int64   `json:"existing_claim_count"`
	NonExistingClaimCount int64   `json:"non_existing_claim_count"`
}

// Topics returns every topic with per-type claim counts, backing the US6/US15
// filter chips.
func (s *ClaimService) Topics(ctx context.Context) ([]TopicView, error) {
	topics, err := s.claims.ListTopics(ctx)
	if err != nil {
		return nil, apperr.Internal("could not load topics").Wrap(err)
	}

	existingCounts, err := s.claims.CountClaimsByTopic(ctx, models.ClaimTypeExisting)
	if err != nil {
		return nil, apperr.Internal("could not count existing claims by topic").Wrap(err)
	}
	nonExistingCounts, err := s.claims.CountClaimsByTopic(ctx, models.ClaimTypeNonExisting)
	if err != nil {
		return nil, apperr.Internal("could not count synthetic claims by topic").Wrap(err)
	}

	out := make([]TopicView, 0, len(topics))
	for _, t := range topics {
		out = append(out, TopicView{
			ID:                    t.ID.String(),
			Name:                  t.Name,
			Description:           t.Description,
			ExistingClaimCount:    existingCounts[t.ID],
			NonExistingClaimCount: nonExistingCounts[t.ID],
		})
	}
	return out, nil
}

// Topic returns a single topic.
func (s *ClaimService) Topic(ctx context.Context, id uuid.UUID) (*TopicView, error) {
	topic, err := s.claims.FindTopicByID(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, apperr.NotFound("topic not found")
		}
		return nil, apperr.Internal("could not load topic").Wrap(err)
	}

	existing := repository.ClaimFilter{ClaimType: models.ClaimTypeExisting, TopicIDs: []uuid.UUID{id}}
	existingCount, err := s.claims.CountClaims(ctx, existing)
	if err != nil {
		return nil, apperr.Internal("could not count existing claims").Wrap(err)
	}
	nonExisting := repository.ClaimFilter{ClaimType: models.ClaimTypeNonExisting, TopicIDs: []uuid.UUID{id}}
	nonExistingCount, err := s.claims.CountClaims(ctx, nonExisting)
	if err != nil {
		return nil, apperr.Internal("could not count synthetic claims").Wrap(err)
	}

	return &TopicView{
		ID:                    topic.ID.String(),
		Name:                  topic.Name,
		Description:           topic.Description,
		ExistingClaimCount:    existingCount,
		NonExistingClaimCount: nonExistingCount,
	}, nil
}

// formatFloat renders a score for storage in cis_settings.
func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}
