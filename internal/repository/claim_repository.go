package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/cis/cis-backend/internal/models"
)

// ClaimRepository reads the AI service's `claims`, `topics`, `content_items`,
// and `claim_policies` tables, overlaying this backend's own
// `cis_claim_reviews` and `cis_claim_alerts`.
//
// It exposes no write method for any AI-owned table. The only mutations here
// target cis_* tables.
type ClaimRepository struct {
	db *gorm.DB
}

// NewClaimRepository constructs a ClaimRepository.
func NewClaimRepository(db *gorm.DB) *ClaimRepository {
	return &ClaimRepository{db: db}
}

// ClaimRow is a claim joined with its topic name and the human review status
// overlay.
type ClaimRow struct {
	models.AIClaim `gorm:"embedded"`
	ReviewStatus   string  `gorm:"column:review_status"`
	TopicName      *string `gorm:"column:topic_name"`
}

// ClaimFilter describes the F1 list/search query (US1, US6, US7, US11, US15,
// US16, US19).
type ClaimFilter struct {
	// ClaimType is the canonical type, models.ClaimTypeExisting or
	// models.ClaimTypeNonExisting. Empty means both.
	ClaimType string
	// ReviewStatus filters on the overlaid status. Empty (or "all") means all.
	ReviewStatus string
	TopicIDs     []uuid.UUID
	Search       string
	// PolicyIDs restricts to claims correlated with these AI policy ids, used
	// by the F2 detail page (US39).
	PolicyIDs []uuid.UUID
	// SortBy is "score" (US7) or "created_at" (US16).
	SortBy string
	Limit  int
	Offset int
}

const (
	// SortByScore ranks by FinalClaimScore descending (US7).
	SortByScore = "score"
	// SortByCreatedAt ranks by newest first (US16).
	SortByCreatedAt = "created_at"
)

// baseQuery builds the shared FROM/JOIN/WHERE for claim listing.
//
// The LEFT JOIN onto cis_claim_reviews is what makes the human status overlay
// work: a claim with no review row resolves to 'unreviewed', and filtering
// happens in SQL so paging stays correct.
func (r *ClaimRepository) baseQuery(ctx context.Context, f ClaimFilter) *gorm.DB {
	q := r.db.WithContext(ctx).
		Table("claims AS c").
		Joins("LEFT JOIN cis_claim_reviews AS rev ON rev.claim_id = c.id").
		Joins("LEFT JOIN topics AS t ON t.id = c.topic_id")

	if f.ClaimType != "" {
		q = q.Where("c.claim_type IN ?", models.ClaimTypeValues(f.ClaimType))
	}
	if f.ReviewStatus != "" && f.ReviewStatus != "all" {
		q = q.Where("COALESCE(rev.status, ?) = ?", models.ReviewStatusUnreviewed, f.ReviewStatus)
	}
	if len(f.TopicIDs) > 0 {
		q = q.Where("c.topic_id IN ?", f.TopicIDs)
	}
	if s := strings.TrimSpace(f.Search); s != "" {
		q = q.Where("c.claim_statement ILIKE ?", "%"+escapeLike(s)+"%")
	}
	if len(f.PolicyIDs) > 0 {
		// A claim relates to a policy either through the many-to-many join
		// table (Existing claims, US12) or through claims.policy_id
		// (Non-Existing claims, US20).
		q = q.Where(
			"(c.policy_id IN ? OR EXISTS (SELECT 1 FROM claim_policies cp WHERE cp.claim_id = c.id AND cp.policy_id IN ?))",
			f.PolicyIDs, f.PolicyIDs,
		)
	}
	return q
}

// ListClaims returns a page of claims matching the filter.
func (r *ClaimRepository) ListClaims(ctx context.Context, f ClaimFilter) ([]ClaimRow, error) {
	var rows []ClaimRow

	q := r.baseQuery(ctx, f).
		Select("c.*, COALESCE(rev.status, ?) AS review_status, t.name AS topic_name", models.ReviewStatusUnreviewed).
		Order(orderClause(f.SortBy))

	if f.Limit > 0 {
		q = q.Limit(f.Limit)
	}
	if f.Offset > 0 {
		q = q.Offset(f.Offset)
	}

	if err := q.Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// CountClaims returns the total number of claims matching the filter, ignoring
// paging.
func (r *ClaimRepository) CountClaims(ctx context.Context, f ClaimFilter) (int64, error) {
	var total int64
	err := r.baseQuery(ctx, f).Select("COUNT(c.id)").Scan(&total).Error
	return total, err
}

// FindClaimByID loads a single claim with its topic and review status.
func (r *ClaimRepository) FindClaimByID(ctx context.Context, id uuid.UUID) (*ClaimRow, error) {
	var row ClaimRow
	err := r.db.WithContext(ctx).
		Table("claims AS c").
		Joins("LEFT JOIN cis_claim_reviews AS rev ON rev.claim_id = c.id").
		Joins("LEFT JOIN topics AS t ON t.id = c.topic_id").
		Select("c.*, COALESCE(rev.status, ?) AS review_status, t.name AS topic_name", models.ReviewStatusUnreviewed).
		Where("c.id = ?", id).
		Limit(1).
		Scan(&row).Error
	if err != nil {
		return nil, err
	}
	if row.ID == uuid.Nil {
		return nil, ErrNotFound
	}
	return &row, nil
}

// ClaimExists reports whether a claim id is present, and returns its raw
// claim_type so callers can enforce type-specific rules (e.g. US26, which bars
// Synthetic claims from the Alert page).
func (r *ClaimRepository) ClaimExists(ctx context.Context, id uuid.UUID) (bool, string, error) {
	var result struct {
		ClaimType string `gorm:"column:claim_type"`
	}
	err := r.db.WithContext(ctx).
		Table("claims").
		Select("claim_type").
		Where("id = ?", id).
		Limit(1).
		Scan(&result).Error
	if err != nil {
		return false, "", err
	}
	if result.ClaimType == "" {
		return false, "", nil
	}
	return true, result.ClaimType, nil
}

// StanceCount is a per-claim tally of statements on one side.
type StanceCount struct {
	ClaimID  uuid.UUID `gorm:"column:claim_id"`
	Positive int64     `gorm:"column:positive"`
	Negative int64     `gorm:"column:negative"`
}

// CountStatementsByClaim tallies supporting and opposing content for a set of
// claims in a single query, avoiding an N+1 across a card list.
//
// Positive = supporting, Negative = opposing (US12). Neutral content is
// excluded from both, mirroring PRD 6.4.2.
func (r *ClaimRepository) CountStatementsByClaim(ctx context.Context, claimIDs []uuid.UUID) (map[uuid.UUID]StanceCount, error) {
	out := make(map[uuid.UUID]StanceCount, len(claimIDs))
	if len(claimIDs) == 0 {
		return out, nil
	}

	var rows []StanceCount
	err := r.db.WithContext(ctx).
		Table("content_items").
		Select(
			"claim_id, "+
				"COUNT(*) FILTER (WHERE stance = ?) AS positive, "+
				"COUNT(*) FILTER (WHERE stance = ?) AS negative",
			models.StanceSupporting, models.StanceOpposing,
		).
		Where("claim_id IN ?", claimIDs).
		Group("claim_id").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	for _, row := range rows {
		out[row.ClaimID] = row
	}
	return out, nil
}

// ListStatements returns a page of source posts for a claim, optionally
// filtered to one stance.
func (r *ClaimRepository) ListStatements(ctx context.Context, claimID uuid.UUID, stance string, limit, offset int) ([]models.AIContentItem, int64, error) {
	base := r.db.WithContext(ctx).Model(&models.AIContentItem{}).Where("claim_id = ?", claimID)
	if stance != "" {
		base = base.Where("stance = ?", stance)
	}

	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var items []models.AIContentItem
	err := base.Order("created_at DESC").Limit(limit).Offset(offset).Find(&items).Error
	if err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// TopAccount is one row of the US12 Top 5 Accounts panel.
type TopAccount struct {
	AuthorID         string `gorm:"column:author_id"`
	ContentCount     int64  `gorm:"column:content_count"`
	TotalImpressions int64  `gorm:"column:total_impressions"`
}

// ListTopAccounts returns the accounts driving a claim's spread (US12).
//
// Scoped to Supporting-side content only, matching the Reach parameter's scope
// in PRD 6.1.1, and ranked by contributed impressions with post count as the
// tiebreaker.
func (r *ClaimRepository) ListTopAccounts(ctx context.Context, claimID uuid.UUID, limit int) ([]TopAccount, error) {
	var rows []TopAccount
	err := r.db.WithContext(ctx).
		Table("content_items").
		Select("author_id, COUNT(*) AS content_count, COALESCE(SUM(impressions), 0) AS total_impressions").
		Where("claim_id = ?", claimID).
		Where("author_id IS NOT NULL AND author_id <> ''").
		Where("stance = ?", models.StanceSupporting).
		Group("author_id").
		Order("total_impressions DESC, content_count DESC, author_id ASC").
		Limit(limit).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// ListPolicyIDsForClaim resolves the AI policy ids correlated with a claim,
// covering both the many-to-many join (US12) and the single policy_id column
// used by Synthetic claims (US20).
func (r *ClaimRepository) ListPolicyIDsForClaim(ctx context.Context, claimID uuid.UUID, directPolicyID *uuid.UUID) ([]uuid.UUID, error) {
	var joined []uuid.UUID
	err := r.db.WithContext(ctx).
		Table("claim_policies").
		Where("claim_id = ?", claimID).
		Pluck("policy_id", &joined).Error
	if err != nil {
		return nil, err
	}

	seen := make(map[uuid.UUID]struct{}, len(joined)+1)
	out := make([]uuid.UUID, 0, len(joined)+1)
	for _, id := range joined {
		if _, dup := seen[id]; !dup {
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	if directPolicyID != nil {
		if _, dup := seen[*directPolicyID]; !dup {
			out = append(out, *directPolicyID)
		}
	}
	return out, nil
}

// UpsertReview records a human status decision in the backend-owned overlay
// table, leaving the AI service's claims.status untouched.
func (r *ClaimRepository) UpsertReview(ctx context.Context, claimID uuid.UUID, status string, notes *string, reviewedBy *uuid.UUID) (*models.CISClaimReview, error) {
	now := time.Now().UTC()

	var review models.CISClaimReview
	err := r.db.WithContext(ctx).Where("claim_id = ?", claimID).First(&review).Error

	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		review = models.CISClaimReview{
			ClaimID:    claimID,
			Status:     status,
			Notes:      notes,
			ReviewedBy: reviewedBy,
			ReviewedAt: now,
		}
		if err := r.db.WithContext(ctx).Create(&review).Error; err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	default:
		updates := map[string]any{
			"status":      status,
			"reviewed_by": reviewedBy,
			"reviewed_at": now,
			"updated_at":  now,
		}
		if notes != nil {
			updates["notes"] = notes
		}
		if err := r.db.WithContext(ctx).Model(&review).Updates(updates).Error; err != nil {
			return nil, err
		}
		review.Status = status
		review.ReviewedAt = now
		review.ReviewedBy = reviewedBy
		if notes != nil {
			review.Notes = notes
		}
	}
	return &review, nil
}

// ListTopics returns every topic for the F1 filter chips (US6, US15).
func (r *ClaimRepository) ListTopics(ctx context.Context) ([]models.AITopic, error) {
	var topics []models.AITopic
	err := r.db.WithContext(ctx).Order("name ASC").Find(&topics).Error
	return topics, err
}

// FindTopicByID loads one topic.
func (r *ClaimRepository) FindTopicByID(ctx context.Context, id uuid.UUID) (*models.AITopic, error) {
	var topic models.AITopic
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&topic).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &topic, nil
}

// TopicClaimCount is a per-topic tally used to annotate the filter chips.
type TopicClaimCount struct {
	TopicID uuid.UUID `gorm:"column:topic_id"`
	Total   int64     `gorm:"column:total"`
}

// CountClaimsByTopic tallies claims per topic for a given claim type.
func (r *ClaimRepository) CountClaimsByTopic(ctx context.Context, claimType string) (map[uuid.UUID]int64, error) {
	var rows []TopicClaimCount
	q := r.db.WithContext(ctx).Table("claims").Select("topic_id, COUNT(*) AS total").Group("topic_id")
	if claimType != "" {
		q = q.Where("claim_type IN ?", models.ClaimTypeValues(claimType))
	}
	if err := q.Scan(&rows).Error; err != nil {
		return nil, err
	}

	out := make(map[uuid.UUID]int64, len(rows))
	for _, row := range rows {
		out[row.TopicID] = row.Total
	}
	return out, nil
}

// orderClause maps a sort key onto a deterministic SQL ORDER BY.
//
// NULLS LAST matters: an unscored claim must never outrank a scored one just
// because Postgres sorts NULL high by default in DESC order.
func orderClause(sortBy string) string {
	switch sortBy {
	case SortByCreatedAt:
		return "c.created_at DESC, c.id DESC"
	default:
		return "c.final_claim_score DESC NULLS LAST, c.created_at DESC, c.id DESC"
	}
}

// escapeLike neutralizes LIKE wildcards in user-supplied search text so a
// query for "100%" does not match everything.
func escapeLike(s string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_")
	return replacer.Replace(s)
}

// GranularityToTrunc maps an API granularity onto a Postgres date_trunc unit.
func GranularityToTrunc(granularity string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(granularity)) {
	case "day":
		return "day", nil
	case "", "week":
		return "week", nil
	case "month":
		return "month", nil
	case "year":
		return "year", nil
	default:
		return "", fmt.Errorf("unsupported granularity %q", granularity)
	}
}
