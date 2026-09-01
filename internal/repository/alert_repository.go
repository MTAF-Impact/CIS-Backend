package repository

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

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

	// Threshold-crossing state (US71). CrossedAt is when this claim last
	// flipped Over/Under; the service compares it against the reader's own
	// acknowledgment to decide whether the row is still highlighted.
	CrossedAt        *time.Time `gorm:"column:crossed_at"`
	CrossedDirection *string    `gorm:"column:crossed_direction"`
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
				"a.crossed_at, a.crossed_direction, "+
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

// --- Threshold-crossing detection (PRD v1.5, US71) ---

// ThresholdState is a watched claim's current score alongside the Over/Under
// status recorded at the previous evaluation.
type ThresholdState struct {
	ClaimID         uuid.UUID `gorm:"column:claim_id"`
	FinalClaimScore *float64  `gorm:"column:final_claim_score"`
	LastStatus      string    `gorm:"column:last_threshold_status"`
}

// ListThresholdStates returns the crossing inputs for the watchlist. An empty
// claimIDs slice means every watched claim.
func (r *AlertRepository) ListThresholdStates(ctx context.Context, claimIDs []uuid.UUID) ([]ThresholdState, error) {
	q := r.db.WithContext(ctx).
		Table("cis_claim_alerts AS a").
		Joins("INNER JOIN claims AS c ON c.id = a.claim_id").
		Select("a.claim_id, a.last_threshold_status, c.final_claim_score")
	if len(claimIDs) > 0 {
		q = q.Where("a.claim_id IN ?", claimIDs)
	}

	var rows []ThresholdState
	err := q.Scan(&rows).Error
	return rows, err
}

// RecordThresholdStatus stores a claim's evaluated Over/Under status.
//
// direction is empty when the status was merely observed — a first evaluation
// seeding the baseline, or a claim that has not moved. Passing a direction
// records the crossing US71 notifies on, and stamps crossed_at.
func (r *AlertRepository) RecordThresholdStatus(
	ctx context.Context, claimID uuid.UUID, status, direction string, at time.Time,
) error {
	updates := map[string]any{"last_threshold_status": status, "updated_at": at}
	if direction != "" {
		updates["crossed_at"] = at
		updates["crossed_direction"] = direction
	}
	return r.db.WithContext(ctx).
		Model(&models.CISClaimAlert{}).
		Where("claim_id = ?", claimID).
		Updates(updates).Error
}

// CountCrossingsSince counts watched claims whose last crossing is newer than
// the reader's acknowledgment, which is the US71 sidebar badge number.
//
// A nil since means the user has never opened F3, so every recorded crossing
// still counts.
func (r *AlertRepository) CountCrossingsSince(ctx context.Context, since *time.Time) (int64, error) {
	q := r.db.WithContext(ctx).
		Model(&models.CISClaimAlert{}).
		Where("crossed_at IS NOT NULL")
	if since != nil {
		q = q.Where("crossed_at > ?", *since)
	}

	var count int64
	err := q.Count(&count).Error
	return count, err
}

// ListCrossingsSince returns the watchlist rows behind that badge, newest
// crossing first, so the notification can name the claims rather than only
// counting them.
func (r *AlertRepository) ListCrossingsSince(ctx context.Context, since *time.Time, limit int) ([]AlertRow, error) {
	q := r.db.WithContext(ctx).
		Table("cis_claim_alerts AS a").
		Joins("INNER JOIN claims AS c ON c.id = a.claim_id").
		Joins("LEFT JOIN cis_claim_reviews AS rev ON rev.claim_id = c.id").
		Joins("LEFT JOIN topics AS t ON t.id = c.topic_id").
		Where("a.crossed_at IS NOT NULL")
	if since != nil {
		q = q.Where("a.crossed_at > ?", *since)
	}

	var rows []AlertRow
	err := q.Select(
		"a.id AS alert_id, a.claim_id, a.chart_visible, a.added_at, "+
			"a.crossed_at, a.crossed_direction, "+
			"c.claim_statement, c.created_at AS claim_created_at, c.final_claim_score, c.is_dormant, c.topic_id, "+
			"COALESCE(rev.status, ?) AS review_status, t.name AS topic_name",
		models.ReviewStatusUnreviewed,
	).
		Order("a.crossed_at DESC").
		Limit(limit).
		Scan(&rows).Error
	return rows, err
}

// AcknowledgedAt returns when a user last opened F3, or nil if never.
func (r *AlertRepository) AcknowledgedAt(ctx context.Context, userID uuid.UUID) (*time.Time, error) {
	var ack models.CISAlertAcknowledgement
	err := r.db.WithContext(ctx).Where("user_id = ?", userID).First(&ack).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &ack.AcknowledgedAt, nil
}

// Acknowledge records that a user has seen the current crossings (US71).
func (r *AlertRepository) Acknowledge(ctx context.Context, userID uuid.UUID, at time.Time) error {
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.Assignments(map[string]any{"acknowledged_at": at, "updated_at": at}),
	}).Create(&models.CISAlertAcknowledgement{
		UserID:         userID,
		AcknowledgedAt: at,
		UpdatedAt:      at,
	}).Error
}
