package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/cis/cis-backend/internal/models"
)

// SnapshotRepository maintains cis_claim_score_snapshots, the backend-owned
// history behind the F3 line chart.
//
// The AI service stores only a claim's *current* score, but US27 plots
// FinalClaimScore over time. Copying scores into our own table gives us that
// history without ever writing to an AI-owned table.
type SnapshotRepository struct {
	db *gorm.DB
}

// NewSnapshotRepository constructs a SnapshotRepository.
func NewSnapshotRepository(db *gorm.DB) *SnapshotRepository {
	return &SnapshotRepository{db: db}
}

// CaptureForClaims snapshots the current scores of the given claims. It reads
// from `claims` and writes only to cis_claim_score_snapshots.
func (r *SnapshotRepository) CaptureForClaims(ctx context.Context, claimIDs []uuid.UUID, at time.Time) (int, error) {
	if len(claimIDs) == 0 {
		return 0, nil
	}

	var claims []models.AIClaim
	if err := r.db.WithContext(ctx).
		Where("id IN ?", claimIDs).
		Find(&claims).Error; err != nil {
		return 0, err
	}
	if len(claims) == 0 {
		return 0, nil
	}

	snapshots := make([]models.CISClaimScoreSnapshot, 0, len(claims))
	for _, c := range claims {
		snapshots = append(snapshots, models.CISClaimScoreSnapshot{
			ID:                         uuid.New(),
			ClaimID:                    c.ID,
			ReachScore:                 c.ReachScore,
			VelocityScore:              c.VelocityScore,
			FalsenessScore:             c.FalsenessScore,
			HarmScore:                  c.HarmScore,
			EmotionalIntensityScore:    c.EmotionalIntensityScore,
			EmotionalIntensityOpposing: c.EmotionalIntensityOpposing,
			ClaimScore:                 c.ClaimScore,
			NPR:                        c.NPR,
			DiscountFactor:             c.DiscountFactor,
			FinalClaimScore:            c.FinalClaimScore,
			IsDormant:                  c.IsDormant,
			CapturedAt:                 at,
		})
	}

	if err := r.db.WithContext(ctx).CreateInBatches(&snapshots, 200).Error; err != nil {
		return 0, err
	}
	return len(snapshots), nil
}

// SeriesPoint is one time bucket of a claim's score history.
type SeriesPoint struct {
	ClaimID         uuid.UUID `gorm:"column:claim_id"`
	BucketStart     time.Time `gorm:"column:bucket_start"`
	FinalClaimScore *float64  `gorm:"column:final_claim_score"`
	ClaimScore      *float64  `gorm:"column:claim_score"`
	SampleCount     int64     `gorm:"column:sample_count"`
}

// SeriesFilter narrows a score-history query.
type SeriesFilter struct {
	ClaimIDs []uuid.UUID
	// Trunc is a validated Postgres date_trunc unit. Callers must obtain it
	// from GranularityToTrunc — it is interpolated into SQL, so an unvalidated
	// value would be an injection vector.
	Trunc string
	From  *time.Time
	To    *time.Time
}

// Series returns bucketed score history for the given claims.
//
// Scores are averaged within each bucket. A bucket can contain several
// snapshots (the capture job runs more often than the chart's coarsest
// granularity), and an average is both cheaper to compute than a
// last-value-per-bucket window function and smoother to plot.
func (r *SnapshotRepository) Series(ctx context.Context, f SeriesFilter) ([]SeriesPoint, error) {
	if len(f.ClaimIDs) == 0 {
		return nil, nil
	}

	trunc := f.Trunc
	if trunc == "" {
		trunc = "week"
	}

	q := r.db.WithContext(ctx).
		Table("cis_claim_score_snapshots").
		Select(
			"claim_id, "+
				"date_trunc(?, captured_at) AS bucket_start, "+
				"AVG(final_claim_score) AS final_claim_score, "+
				"AVG(claim_score) AS claim_score, "+
				"COUNT(*) AS sample_count",
			trunc,
		).
		Where("claim_id IN ?", f.ClaimIDs).
		Group("claim_id, bucket_start").
		Order("bucket_start ASC")

	if f.From != nil {
		q = q.Where("captured_at >= ?", *f.From)
	}
	if f.To != nil {
		q = q.Where("captured_at <= ?", *f.To)
	}

	var points []SeriesPoint
	if err := q.Scan(&points).Error; err != nil {
		return nil, err
	}
	return points, nil
}

// DeleteOlderThan prunes snapshot history beyond the retention window.
func (r *SnapshotRepository) DeleteOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	res := r.db.WithContext(ctx).
		Where("captured_at < ?", cutoff).
		Delete(&models.CISClaimScoreSnapshot{})
	return res.RowsAffected, res.Error
}
