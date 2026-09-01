package repository

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/cis/cis-backend/internal/models"
)

// OverviewRepository reads the aggregates behind F6, the Overview page
// (PRD v1.5, Section 11).
//
// Every query here is a read over AI-owned tables. Nothing on this page is
// stored: the ratio, the treemap and the leaderboard are derived from the
// current claim table on each request, and the Climate Sentiment Index is
// derived from the content stream. Materialising them would introduce a second
// number that could disagree with F1's own ranking.
//
// # Capability probing
//
// PRD v1.5 needs two columns on content_items that v1.4's schema does not have:
// `sentiment`, without which BCS cannot be computed at all, and `city`, without
// which US65's city selection cannot partition anything. Both are AI-owned, so
// this backend cannot add them. Rather than fail every F6 request on a
// database where the AI service has not shipped them yet, their presence is
// probed once at construction and the queries adapt: O1 reports
// insufficient_data, and city scoping degrades to the whole instance — which is
// what the single-city deployment model of PRD 6.6.4 means in practice anyway.
type OverviewRepository struct {
	db *gorm.DB

	// hasSentiment and hasCity are fixed at construction. They describe the
	// schema, which does not change while the process is running; re-probing
	// per request would add a catalog query to every page load to detect a
	// migration that requires a deploy on the other side regardless.
	hasSentiment bool
	hasCity      bool
}

// NewOverviewRepository constructs an OverviewRepository, probing for the
// PRD v1.5 columns on content_items.
func NewOverviewRepository(db *gorm.DB) *OverviewRepository {
	r := &OverviewRepository{db: db}
	if db.Migrator().HasTable("content_items") {
		r.hasSentiment = db.Migrator().HasColumn("content_items", "sentiment")
		r.hasCity = db.Migrator().HasColumn("content_items", "city")
	}
	return r
}

// HasSentiment reports whether content_items carries the sentiment column the
// Climate Sentiment Index needs (PRD 6.6.1).
func (r *OverviewRepository) HasSentiment() bool { return r.hasSentiment }

// HasCity reports whether content_items is tagged with a city, and so whether
// the US65 selection actually partitions the data or only labels it.
func (r *OverviewRepository) HasCity() bool { return r.hasCity }

// ThresholdCounts is the O1 above/below-threshold split (US67).
type ThresholdCounts struct {
	Above int64 `gorm:"column:above"`
	Below int64 `gorm:"column:below"`
	Total int64 `gorm:"column:total"`
}

// ThresholdCounts counts Existing/Generic claims either side of the F4
// threshold (US67).
//
// Every S1 claim is counted regardless of review status, per the assumption
// US67 flags: the ratio is a picture of the information environment, not of the
// team's triage queue, and excluding Inactive or Action Taken claims would make
// the number improve whenever someone closed a ticket.
//
// A claim with no score counts as below. Escalating on missing data is the one
// direction that cannot be justified to a reviewer.
func (r *OverviewRepository) ThresholdCounts(ctx context.Context, city string, threshold float64) (ThresholdCounts, error) {
	var out ThresholdCounts
	err := r.claimScope(ctx, city).
		Select(
			"COUNT(*) FILTER (WHERE c.final_claim_score >= ?) AS above, "+
				"COUNT(*) FILTER (WHERE c.final_claim_score IS NULL OR c.final_claim_score < ?) AS below, "+
				"COUNT(*) AS total",
			threshold, threshold,
		).
		Scan(&out).Error
	return out, err
}

// TopicAggregate is one rectangle of the O2 treemap (US69).
type TopicAggregate struct {
	TopicID    uuid.UUID `gorm:"column:topic_id"`
	TopicName  string    `gorm:"column:topic_name"`
	ClaimCount int64     `gorm:"column:claim_count"`
	AboveCount int64     `gorm:"column:above_count"`
	AvgScore   *float64  `gorm:"column:avg_score"`
}

// TopicAggregates returns per-topic claim counts and average score (US69).
//
// Existing/Generic topics only: US69 excludes Synthetic-claim topics, because a
// treemap sized by predicted claims would compete for attention with one sized
// by claims that actually exist.
func (r *OverviewRepository) TopicAggregates(ctx context.Context, city string, threshold float64) ([]TopicAggregate, error) {
	var out []TopicAggregate
	err := r.claimScope(ctx, city).
		Joins("INNER JOIN topics AS t ON t.id = c.topic_id").
		Select(
			"t.id AS topic_id, t.name AS topic_name, COUNT(*) AS claim_count, "+
				"COUNT(*) FILTER (WHERE c.final_claim_score >= ?) AS above_count, "+
				"AVG(c.final_claim_score) AS avg_score",
			threshold,
		).
		Group("t.id, t.name").
		Scan(&out).Error
	return out, err
}

// TopicAggregate returns one topic's aggregate, for the O2 click-through modal.
func (r *OverviewRepository) TopicAggregate(ctx context.Context, city string, topicID uuid.UUID, threshold float64) (*TopicAggregate, error) {
	var out TopicAggregate
	err := r.claimScope(ctx, city).
		Joins("INNER JOIN topics AS t ON t.id = c.topic_id").
		Where("t.id = ?", topicID).
		Select(
			"t.id AS topic_id, t.name AS topic_name, COUNT(*) AS claim_count, "+
				"COUNT(*) FILTER (WHERE c.final_claim_score >= ?) AS above_count, "+
				"AVG(c.final_claim_score) AS avg_score",
			threshold,
		).
		Group("t.id, t.name").
		Scan(&out).Error
	if err != nil {
		return nil, err
	}
	if out.TopicID == uuid.Nil {
		return nil, ErrNotFound
	}
	return &out, nil
}

// PolicyAggregate is one row of the O3 leaderboard (US70).
type PolicyAggregate struct {
	AIPolicyID uuid.UUID `gorm:"column:policy_id"`
	ClaimCount int64     `gorm:"column:claim_count"`
	AboveCount int64     `gorm:"column:above_count"`
	AvgScore   *float64  `gorm:"column:avg_score"`
}

// PolicyAggregates returns per-policy claim counts and average score (US70).
//
// A claim reaches a policy two ways: the many-to-many join table (US12) and the
// direct claims.policy_id column (US20). Both are read, and the UNION
// deduplicates a claim linked both ways so it is never counted twice.
//
// Only Existing/Generic claims are counted, per US70's "correlated
// Existing-claims" — claimScope applies that filter.
func (r *OverviewRepository) PolicyAggregates(ctx context.Context, city string, threshold float64) ([]PolicyAggregate, error) {
	var out []PolicyAggregate
	err := r.claimScope(ctx, city).
		Joins(
			"INNER JOIN (SELECT claim_id, policy_id FROM claim_policies "+
				"UNION SELECT id AS claim_id, policy_id FROM claims WHERE policy_id IS NOT NULL) AS l "+
				"ON l.claim_id = c.id").
		Select(
			"l.policy_id AS policy_id, COUNT(*) AS claim_count, "+
				"COUNT(*) FILTER (WHERE c.final_claim_score >= ?) AS above_count, "+
				"AVG(c.final_claim_score) AS avg_score",
			threshold,
		).
		Group("l.policy_id").
		Scan(&out).Error
	return out, err
}

// ConversationVolumes is the BCS numerator and denominator (PRD 6.6.1).
type ConversationVolumes struct {
	Total    int64 `gorm:"column:total"`
	Positive int64 `gorm:"column:positive"`
	Negative int64 `gorm:"column:negative"`
	Neutral  int64 `gorm:"column:neutral"`
}

// ConversationVolumes counts climate conversation by sentiment over a window
// (PRD 6.6.1).
//
// The denominator is every content item in the window, whether or not it was
// ever clustered into a claim — PRD 6.6.1 is explicit that
// TotalClimateConversationVolume is "independent of the claim repository", and
// counting only clustered content would make the index improve every time the
// pipeline failed to cluster something.
//
// Returns a zero struct when the sentiment column is absent; callers surface
// that as insufficient_data.
func (r *OverviewRepository) ConversationVolumes(ctx context.Context, city string, from, to time.Time) (ConversationVolumes, error) {
	var out ConversationVolumes
	if !r.hasSentiment {
		return out, nil
	}

	q := r.contentScope(ctx, city).Where("created_at >= ? AND created_at < ?", from, to)
	err := q.Select(
		"COUNT(*) AS total, " +
			"COUNT(*) FILTER (WHERE sentiment = 'positive') AS positive, " +
			"COUNT(*) FILTER (WHERE sentiment = 'negative') AS negative, " +
			"COUNT(*) FILTER (WHERE sentiment = 'neutral') AS neutral",
	).Scan(&out).Error
	return out, err
}

// WeightedRiskScore returns Σ(FinalClaimScore × Volume) over claims scoring at
// or above the risk threshold (PRD 6.6.2).
//
// Volume is the claim's combined Supporting-plus-Opposing conversation in the
// window. Neutral content is excluded because PRD 6.6.2 defines Volume_i as the
// two stance sides; it stays in the BCS denominator, where the definition is
// explicitly "all climate-related content".
func (r *OverviewRepository) WeightedRiskScore(ctx context.Context, city string, from, to time.Time, riskThreshold float64) (float64, error) {
	volumes := r.contentScope(ctx, city).
		Select("claim_id, COUNT(*) AS volume").
		Where("claim_id IS NOT NULL").
		Where("stance IN ?", []string{models.StanceSupporting, models.StanceOpposing}).
		Where("created_at >= ? AND created_at < ?", from, to).
		Group("claim_id")

	var sum *float64
	err := r.db.WithContext(ctx).
		Table("claims AS c").
		Joins("INNER JOIN (?) AS v ON v.claim_id = c.id", volumes).
		Where("c.final_claim_score >= ?", riskThreshold).
		Select("SUM(c.final_claim_score * v.volume)").
		Scan(&sum).Error
	if err != nil || sum == nil {
		return 0, err
	}
	return *sum, nil
}

// TopicScoreAverage is a topic's mean FinalClaimScore over a snapshot window,
// used for the O2 modal's month-on-month change (US69).
//
// It reads the AI service's claim_score_snapshots, which records a row per
// rescore for every claim, rather than the backend's own table, which only
// covers watchlisted claims. A month-on-month figure computed over the
// watchlist alone would describe the team's attention, not the topic.
//
// Best-effort: a database without the AI snapshot table returns nil rather than
// an error, and the modal simply shows no change indicator.
func (r *OverviewRepository) TopicScoreAverage(ctx context.Context, topicID uuid.UUID, from, to time.Time) (*float64, error) {
	var avg *float64
	err := r.db.WithContext(ctx).
		Table("claim_score_snapshots AS s").
		Joins("INNER JOIN claims AS c ON c.id = s.claim_id").
		Where("c.topic_id = ?", topicID).
		Where("c.claim_type IN ?", models.ExistingClaimTypeValues).
		Where("s.recorded_at >= ? AND s.recorded_at < ?", from, to).
		Select("AVG(s.final_claim_score)").
		Scan(&avg).Error
	if err != nil {
		if errIsPipelineUnavailable(err) {
			log.Printf("[overview] AI score snapshots unavailable, month-on-month change omitted: %v", err)
			return nil, nil
		}
		return nil, err
	}
	return avg, nil
}

// claimScope is the shared FROM/WHERE for every claim-side F6 aggregate:
// Existing/Generic claims, narrowed to the configured city where the data
// supports it.
func (r *OverviewRepository) claimScope(ctx context.Context, city string) *gorm.DB {
	q := r.db.WithContext(ctx).
		Table("claims AS c").
		Where("c.claim_type IN ?", models.ExistingClaimTypeValues)

	// A claim belongs to the configured city when any of the content backing it
	// does. Without a city column on content_items there is no city dimension
	// anywhere in the AI schema, so the instance is the scope — which is the
	// single-city deployment PRD 6.6.4 describes.
	if r.hasCity && city != "" {
		q = q.Where(
			"EXISTS (SELECT 1 FROM content_items ci WHERE ci.claim_id = c.id AND ci.city = ?)",
			city)
	}
	return q
}

// contentScope is the shared FROM/WHERE for the content-side aggregates.
func (r *OverviewRepository) contentScope(ctx context.Context, city string) *gorm.DB {
	q := r.db.WithContext(ctx).Table("content_items")
	if r.hasCity && city != "" {
		q = q.Where("city = ?", city)
	}
	return q
}

// errIsPipelineUnavailable reports whether an error is a missing AI-owned
// relation, reusing the classifier the F5 repository already defines.
func errIsPipelineUnavailable(err error) bool {
	return errors.Is(classify(err), ErrPipelineUnavailable)
}
