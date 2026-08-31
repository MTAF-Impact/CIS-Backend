package repository

import (
	"context"
	"log"
	"sort"
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

// bucketAggregate is one time bucket as the database returns it, before the two
// sources are merged. Sums and counts rather than averages, so two bucket
// aggregates from different tables can be combined without weighting error.
type bucketAggregate struct {
	ClaimID     uuid.UUID `gorm:"column:claim_id"`
	BucketStart time.Time `gorm:"column:bucket_start"`
	FinalSum    *float64  `gorm:"column:final_sum"`
	FinalCount  int64     `gorm:"column:final_count"`
	ClaimSum    *float64  `gorm:"column:claim_sum"`
	ClaimCount  int64     `gorm:"column:claim_count"`
	SampleCount int64     `gorm:"column:sample_count"`
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
// # Two sources
//
// The backend's own cis_claim_score_snapshots is captured hourly and only for
// claims on the watchlist, because snapshotting every claim every hour would
// grow without bound. But GET /claims/:id/score-history is offered on every
// claim, so for the great majority — never bell-icon'd — that table is empty.
//
// The AI service's claim_score_snapshots has the opposite shape: a row for
// every claim it rescores, appended at the moment of the rescore rather than
// sampled on a clock. Reading it is allowed (it is a SELECT on an AI-owned
// table) and it is strictly the richer history, so both are read and merged.
//
// The AI table is read best-effort: a deployment whose AI tables are not
// provisioned yet still gets its own history rather than an error.
//
// # Merging
//
// Scores are averaged within each bucket. A bucket can hold several snapshots —
// the capture job runs more often than the chart's coarsest granularity — and
// an average is both cheaper than a last-value-per-bucket window function and
// smoother to plot. Averaging happens after the merge, over the summed values
// from both tables, so a bucket with three backend rows and one AI row weights
// them equally rather than averaging two averages.
//
// The AI table has no claim_score column, only the final value, so AI rows
// contribute to final_claim_score alone.
func (r *SnapshotRepository) Series(ctx context.Context, f SeriesFilter) ([]SeriesPoint, error) {
	if len(f.ClaimIDs) == 0 {
		return nil, nil
	}

	trunc := f.Trunc
	if trunc == "" {
		trunc = "week"
	}

	buckets, err := r.aggregate(ctx, f, trunc, "cis_claim_score_snapshots", "captured_at", true)
	if err != nil {
		return nil, err
	}

	aiBuckets, err := r.aggregate(ctx, f, trunc, "claim_score_snapshots", "recorded_at", false)
	if err != nil {
		// Never fatal: the backend is designed to serve F1/F3 against a database
		// where the AI service has not provisioned its tables yet.
		log.Printf("[snapshots] could not read the AI service's score history, "+
			"falling back to backend snapshots only: %v", err)
	} else {
		buckets = append(buckets, aiBuckets...)
	}

	return mergeBuckets(buckets), nil
}

// aggregate sums one source table into per-claim, per-bucket totals.
//
// withClaimScore is false for the AI service's table, which records only the
// final score.
func (r *SnapshotRepository) aggregate(
	ctx context.Context,
	f SeriesFilter,
	trunc, table, timeColumn string,
	withClaimScore bool,
) ([]bucketAggregate, error) {
	// Explicitly typed so the driver never has to guess an untyped NULL's type.
	claimSum, claimCount := "NULL::double precision AS claim_sum", "0 AS claim_count"
	if withClaimScore {
		claimSum, claimCount = "SUM(claim_score) AS claim_sum", "COUNT(claim_score) AS claim_count"
	}

	q := r.db.WithContext(ctx).
		Table(table).
		Select(
			"claim_id, "+
				"date_trunc(?, "+timeColumn+") AS bucket_start, "+
				"SUM(final_claim_score) AS final_sum, "+
				"COUNT(final_claim_score) AS final_count, "+
				claimSum+", "+claimCount+", "+
				"COUNT(*) AS sample_count",
			trunc,
		).
		Where("claim_id IN ?", f.ClaimIDs).
		Group("claim_id, bucket_start")

	if f.From != nil {
		q = q.Where(timeColumn+" >= ?", *f.From)
	}
	if f.To != nil {
		q = q.Where(timeColumn+" <= ?", *f.To)
	}

	var out []bucketAggregate
	if err := q.Scan(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// mergeBuckets folds both sources' aggregates into one chronological series,
// collapsing each (claim, bucket) pair into a single averaged point.
func mergeBuckets(aggregates []bucketAggregate) []SeriesPoint {
	type key struct {
		claimID uuid.UUID
		bucket  time.Time
	}

	merged := make(map[key]*bucketAggregate, len(aggregates))
	order := make([]key, 0, len(aggregates))

	for i := range aggregates {
		a := aggregates[i]
		k := key{a.ClaimID, a.BucketStart}
		existing, ok := merged[k]
		if !ok {
			copied := a
			merged[k] = &copied
			order = append(order, k)
			continue
		}
		existing.FinalSum = addPtr(existing.FinalSum, a.FinalSum)
		existing.FinalCount += a.FinalCount
		existing.ClaimSum = addPtr(existing.ClaimSum, a.ClaimSum)
		existing.ClaimCount += a.ClaimCount
		existing.SampleCount += a.SampleCount
	}

	sort.Slice(order, func(i, j int) bool {
		if order[i].bucket.Equal(order[j].bucket) {
			return order[i].claimID.String() < order[j].claimID.String()
		}
		return order[i].bucket.Before(order[j].bucket)
	})

	points := make([]SeriesPoint, 0, len(order))
	for _, k := range order {
		a := merged[k]
		points = append(points, SeriesPoint{
			ClaimID:         a.ClaimID,
			BucketStart:     a.BucketStart,
			FinalClaimScore: average(a.FinalSum, a.FinalCount),
			ClaimScore:      average(a.ClaimSum, a.ClaimCount),
			SampleCount:     a.SampleCount,
		})
	}
	return points
}

// average divides a bucket's summed score by the number of non-null values that
// went into it, returning nil when there were none.
func average(sum *float64, count int64) *float64 {
	if sum == nil || count == 0 {
		return nil
	}
	avg := *sum / float64(count)
	return &avg
}

// addPtr sums two nullable totals, treating nil as "contributed nothing".
func addPtr(a, b *float64) *float64 {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	sum := *a + *b
	return &sum
}

// DeleteOlderThan prunes snapshot history beyond the retention window.
func (r *SnapshotRepository) DeleteOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	res := r.db.WithContext(ctx).
		Where("captured_at < ?", cutoff).
		Delete(&models.CISClaimScoreSnapshot{})
	return res.RowsAffected, res.Error
}
