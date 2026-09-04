package repository

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/cis/cis-backend/internal/models"
)

// PolicyRepository manages cis_policies, the backend-owned Public Policy
// Bank, and reads the AI service's `policies` / `claim_policies` tables to
// resolve correlations.
//
// The backend never inserts into the AI's `policies` table. A cis_policies row
// carries a nullable ai_policy_id soft reference that the AI service fills in
// once matchmaking completes; every correlation query joins through it.
type PolicyRepository struct {
	db *gorm.DB
}

// NewPolicyRepository constructs a PolicyRepository.
func NewPolicyRepository(db *gorm.DB) *PolicyRepository {
	return &PolicyRepository{db: db}
}

// PolicyRow is a policy joined with its claim-activity metadata.
type PolicyRow struct {
	models.CISPolicy `gorm:"embedded"`
	// LastClaimActivityAt is the newest created_at across every claim linked to
	// this policy, and is what the list is sorted by.
	LastClaimActivityAt *time.Time `gorm:"column:last_claim_activity_at"`
	LinkedClaimCount    int64      `gorm:"column:linked_claim_count"`
}

// PolicyFilter describes the policy list query.
type PolicyFilter struct {
	Years  []int
	Search string
	Status string
	Limit  int
	Offset int
}

// claimActivitySubquery aggregates, per AI policy id, the newest linked-claim
// timestamp and the number of linked claims.
//
// A claim links to a policy either through claim_policies (many-to-many,
// Existing claims) or through claims.policy_id (one-to-many, Synthetic claims),
// so both paths are unioned.
const claimActivitySubquery = `
	SELECT policy_id, MAX(created_at) AS last_activity, COUNT(*) AS linked_count
	FROM (
		SELECT cp.policy_id AS policy_id, c.created_at AS created_at
		FROM claim_policies cp
		INNER JOIN claims c ON c.id = cp.claim_id
		UNION ALL
		SELECT c.policy_id AS policy_id, c.created_at AS created_at
		FROM claims c
		WHERE c.policy_id IS NOT NULL
	) linked
	GROUP BY policy_id
`

func (r *PolicyRepository) baseQuery(ctx context.Context, f PolicyFilter) *gorm.DB {
	q := r.db.WithContext(ctx).
		Table("cis_policies AS p").
		Joins("LEFT JOIN (" + claimActivitySubquery + ") AS act ON act.policy_id = p.ai_policy_id")

	if len(f.Years) > 0 {
		q = q.Where("EXTRACT(YEAR FROM p.rolled_out_date) IN ?", f.Years)
	}
	if s := strings.TrimSpace(f.Search); s != "" {
		q = q.Where("p.name ILIKE ?", "%"+escapeLike(s)+"%")
	}
	if f.Status != "" {
		q = q.Where("p.status = ?", f.Status)
	}
	return q
}

// List returns a page of policies ordered by the newest linked-claim date, with
// policies that have no linked claims falling back to their own creation date
// and sorting after all policies that do have activity.
func (r *PolicyRepository) List(ctx context.Context, f PolicyFilter) ([]PolicyRow, int64, error) {
	var total int64
	if err := r.baseQuery(ctx, f).Select("COUNT(p.id)").Scan(&total).Error; err != nil {
		return nil, 0, err
	}

	var rows []PolicyRow
	err := r.baseQuery(ctx, f).
		Select("p.*, act.last_activity AS last_claim_activity_at, COALESCE(act.linked_count, 0) AS linked_claim_count").
		// NULLS LAST puts every policy without claim activity after those with
		// it; the secondary key then orders that group by its own created_at.
		Order("act.last_activity DESC NULLS LAST, p.created_at DESC, p.id DESC").
		Limit(f.Limit).
		Offset(f.Offset).
		Scan(&rows).Error
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// FindByID loads one policy with its activity metadata.
func (r *PolicyRepository) FindByID(ctx context.Context, id uuid.UUID) (*PolicyRow, error) {
	var row PolicyRow
	err := r.db.WithContext(ctx).
		Table("cis_policies AS p").
		Joins("LEFT JOIN ("+claimActivitySubquery+") AS act ON act.policy_id = p.ai_policy_id").
		Select("p.*, act.last_activity AS last_claim_activity_at, COALESCE(act.linked_count, 0) AS linked_claim_count").
		Where("p.id = ?", id).
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

// Create inserts a new policy record.
func (r *PolicyRepository) Create(ctx context.Context, policy *models.CISPolicy) error {
	return r.db.WithContext(ctx).Create(policy).Error
}

// Update applies a partial update to a policy.
func (r *PolicyRepository) Update(ctx context.Context, id uuid.UUID, updates map[string]any) error {
	updates["updated_at"] = time.Now().UTC()
	return r.db.WithContext(ctx).
		Model(&models.CISPolicy{}).
		Where("id = ?", id).
		Updates(updates).Error
}

// Delete removes a policy record. The stored document is deleted separately by
// the service so a storage failure cannot orphan the row.
func (r *PolicyRepository) Delete(ctx context.Context, id uuid.UUID) (int64, error) {
	res := r.db.WithContext(ctx).Where("id = ?", id).Delete(&models.CISPolicy{})
	return res.RowsAffected, res.Error
}

// ListYears returns the distinct rolled-out years present in the bank, for the
// year filter chips.
func (r *PolicyRepository) ListYears(ctx context.Context) ([]int, error) {
	var years []int
	err := r.db.WithContext(ctx).
		Table("cis_policies").
		Distinct().
		Order("year DESC").
		Pluck("EXTRACT(YEAR FROM rolled_out_date)::int AS year", &years).Error
	return years, err
}

// FindPendingMatchmaking returns policies whose AI matchmaking has not yet
// completed, used to retry stuck jobs.
//
// Three states qualify:
//
//   - pending — queued but never handed off (the process died between the
//     insert and the background goroutine, say).
//   - failed — the hand-off itself errored.
//   - processing, but not touched since staleBefore — the AI service acked and
//     the matchmaking callback never arrived. This one is the important case:
//     "processing" is otherwise a terminal state on the backend side, because
//     only the callback moves it, and the AI service never retries a callback.
//
// updated_at is maintained by Update, which every step of the matchmaking
// lifecycle goes through, so it is an accurate "last progress" marker.
func (r *PolicyRepository) FindPendingMatchmaking(
	ctx context.Context,
	maxAttempts int,
	limit int,
	staleBefore time.Time,
) ([]models.CISPolicy, error) {
	var policies []models.CISPolicy
	err := r.db.WithContext(ctx).
		Where(
			"(processing_status IN ? OR (processing_status = ? AND updated_at < ?))",
			[]string{models.ProcessingPending, models.ProcessingFailed},
			models.ProcessingInProgress,
			staleBefore,
		).
		Where("processing_attempts < ?", maxAttempts).
		Order("created_at ASC").
		Limit(limit).
		Find(&policies).Error
	return policies, err
}

// FindAIPoliciesByIDs loads AI-owned policy records by id, used to describe
// correlations for policies this backend did not create.
func (r *PolicyRepository) FindAIPoliciesByIDs(ctx context.Context, ids []uuid.UUID) ([]models.AIPolicy, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var policies []models.AIPolicy
	err := r.db.WithContext(ctx).Where("id IN ?", ids).Find(&policies).Error
	return policies, err
}

// FindByAIPolicyIDs loads the cis_policies rows that shadow the given AI policy
// ids, so a claim's correlated policies can be presented with their policy
// metadata (rollout status, document availability) when we have it.
func (r *PolicyRepository) FindByAIPolicyIDs(ctx context.Context, ids []uuid.UUID) ([]models.CISPolicy, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var policies []models.CISPolicy
	err := r.db.WithContext(ctx).Where("ai_policy_id IN ?", ids).Find(&policies).Error
	return policies, err
}

// FindByAIPolicyID loads the single cis_policies row shadowing an AI policy id.
func (r *PolicyRepository) FindByAIPolicyID(ctx context.Context, id uuid.UUID) (*models.CISPolicy, error) {
	var policy models.CISPolicy
	err := r.db.WithContext(ctx).Where("ai_policy_id = ?", id).First(&policy).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &policy, nil
}

// CountAIPolicies returns the total number of rows in the AI service's
// `policies` table, used as context for a reconciliation sweep.
func (r *PolicyRepository) CountAIPolicies(ctx context.Context) (int64, error) {
	var total int64
	err := r.db.WithContext(ctx).Table("policies").Count(&total).Error
	return total, err
}

// danglingAIPolicyLink matches a cis_policies row whose ai_policy_id points at
// a policy the AI service no longer has. There is no foreign key to cascade —
// ai_policy_id is deliberately a soft reference — so these survive an AI-side
// reset and leave the policy detail page showing a completed badge above empty
// claim lists.
const danglingAIPolicyLink = `ai_policy_id IS NOT NULL AND NOT EXISTS (
	SELECT 1 FROM policies p WHERE p.id = cis_policies.ai_policy_id
)`

// CountDanglingAIPolicyLinks reports how many policies lost their AI record.
func (r *PolicyRepository) CountDanglingAIPolicyLinks(ctx context.Context) (int64, error) {
	var total int64
	err := r.db.WithContext(ctx).
		Model(&models.CISPolicy{}).
		Where(danglingAIPolicyLink).
		Count(&total).Error
	return total, err
}

// ClearDanglingAIPolicyLinks drops the broken link and re-queues matchmaking, so
// the correlations can be rebuilt rather than staying permanently empty behind a
// "completed" badge.
func (r *PolicyRepository) ClearDanglingAIPolicyLinks(ctx context.Context, requeueStatus string) error {
	return r.db.WithContext(ctx).
		Model(&models.CISPolicy{}).
		Where(danglingAIPolicyLink).
		Updates(map[string]any{
			"ai_policy_id":        nil,
			"processing_status":   requeueStatus,
			"processing_attempts": 0,
			"processing_error":    nil,
			"processed_at":        nil,
			"updated_at":          time.Now().UTC(),
		}).Error
}
