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

// AlertRepository manages the F3 watchlist (cis_claim_alerts) and reads the
// claim data needed to render it.
type AlertRepository struct {
	db *gorm.DB
}

// NewAlertRepository constructs an AlertRepository.
func NewAlertRepository(db *gorm.DB) *AlertRepository {
	return &AlertRepository{db: db}
}

// AlertRow is a watchlist entry joined with the claim it watches.
type AlertRow struct {
	AlertID         uuid.UUID  `gorm:"column:alert_id"`
	ClaimID         uuid.UUID  `gorm:"column:claim_id"`
	ChartVisible    bool       `gorm:"column:chart_visible"`
	AddedAt         time.Time  `gorm:"column:added_at"`
	ClaimStatement  string     `gorm:"column:claim_statement"`
	ClaimCreatedAt  time.Time  `gorm:"column:claim_created_at"`
	FinalClaimScore *float64   `gorm:"column:final_claim_score"`
	IsDormant       bool       `gorm:"column:is_dormant"`
	ReviewStatus    string     `gorm:"column:review_status"`
	TopicName       *string    `gorm:"column:topic_name"`
	TopicID         *uuid.UUID `gorm:"column:topic_id"`
}

// ListAlerts returns the watchlist ordered by most recently appended first
// (US30), optionally filtered by a claim-statement search (US31).
func (r *AlertRepository) ListAlerts(ctx context.Context, search string, limit, offset int) ([]AlertRow, int64, error) {
	base := r.db.WithContext(ctx).
		Table("cis_claim_alerts AS a").
		Joins("INNER JOIN claims AS c ON c.id = a.claim_id").
		Joins("LEFT JOIN cis_claim_reviews AS rev ON rev.claim_id = c.id").
		Joins("LEFT JOIN topics AS t ON t.id = c.topic_id")

	if s := strings.TrimSpace(search); s != "" {
		base = base.Where("c.claim_statement ILIKE ?", "%"+escapeLike(s)+"%")
	}

	var total int64
	if err := base.Session(&gorm.Session{}).Select("COUNT(a.id)").Scan(&total).Error; err != nil {
		return nil, 0, err
	}

	var rows []AlertRow
	err := base.Session(&gorm.Session{}).
		Select(
			"a.id AS alert_id, a.claim_id, a.chart_visible, a.added_at, "+
				"c.claim_statement, c.created_at AS claim_created_at, c.final_claim_score, c.is_dormant, c.topic_id, "+
				"COALESCE(rev.status, ?) AS review_status, t.name AS topic_name",
			models.ReviewStatusUnreviewed,
		).
		Order("a.added_at DESC, a.id DESC").
		Limit(limit).
		Offset(offset).
		Scan(&rows).Error
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// FindByClaimID loads the watchlist entry for a claim.
func (r *AlertRepository) FindByClaimID(ctx context.Context, claimID uuid.UUID) (*models.CISClaimAlert, error) {
	var alert models.CISClaimAlert
	err := r.db.WithContext(ctx).Where("claim_id = ?", claimID).First(&alert).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &alert, nil
}

// Create appends a claim to the watchlist.
func (r *AlertRepository) Create(ctx context.Context, alert *models.CISClaimAlert) error {
	return r.db.WithContext(ctx).Create(alert).Error
}

// DeleteByClaimID removes a claim from the watchlist. Because the row carries
// chart_visible, deleting it also unchecks the claim from the chart, which is
// exactly what US14's "Remove" requires.
func (r *AlertRepository) DeleteByClaimID(ctx context.Context, claimID uuid.UUID) (int64, error) {
	res := r.db.WithContext(ctx).Where("claim_id = ?", claimID).Delete(&models.CISClaimAlert{})
	return res.RowsAffected, res.Error
}

// SetChartVisible toggles the [C3] chart checkbox for a watched claim (US28).
func (r *AlertRepository) SetChartVisible(ctx context.Context, claimID uuid.UUID, visible bool) (int64, error) {
	res := r.db.WithContext(ctx).
		Model(&models.CISClaimAlert{}).
		Where("claim_id = ?", claimID).
		Updates(map[string]any{"chart_visible": visible, "updated_at": time.Now().UTC()})
	return res.RowsAffected, res.Error
}

// ListChartClaimIDs returns the claims currently checked for the chart (US28).
func (r *AlertRepository) ListChartClaimIDs(ctx context.Context) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	err := r.db.WithContext(ctx).
		Model(&models.CISClaimAlert{}).
		Where("chart_visible = ?", true).
		Order("added_at DESC").
		Pluck("claim_id", &ids).Error
	return ids, err
}

// AlertedClaimIDs returns which of the given claims are on the watchlist,
// driving the bell icon's filled/outline state on cards (US14).
func (r *AlertRepository) AlertedClaimIDs(ctx context.Context, claimIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
	out := make(map[uuid.UUID]bool, len(claimIDs))
	if len(claimIDs) == 0 {
		return out, nil
	}

	var ids []uuid.UUID
	err := r.db.WithContext(ctx).
		Model(&models.CISClaimAlert{}).
		Where("claim_id IN ?", claimIDs).
		Pluck("claim_id", &ids).Error
	if err != nil {
		return nil, err
	}

	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}
