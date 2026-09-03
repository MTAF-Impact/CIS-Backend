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

// ErrPipelineUnavailable reports that the F5 detection tables do not exist yet.
//
// They are provisioned by the AI service, not by this backend's AutoMigrate, so
// on a database where the detector has never been deployed every F5 query fails
// with a Postgres "relation ... does not exist" (SQLSTATE 42P01). Surfacing
// that verbatim as a 500 would read as a backend bug; it is a deployment state.
// Services translate this into a 503 that says so.
var ErrPipelineUnavailable = errors.New("coordinated-network detection tables are not provisioned")

// undefinedTable is the Postgres SQLSTATE for "relation does not exist".
const undefinedTable = "42P01"

// classify maps a driver error onto ErrPipelineUnavailable when the cause is a
// missing detector table, and passes everything else through untouched.
func classify(err error) error {
	if err == nil {
		return nil
	}
	// pgx surfaces the SQLSTATE in the message; matching on the code rather
	// than on prose keeps this working across driver versions and locales.
	if strings.Contains(err.Error(), undefinedTable) || strings.Contains(err.Error(), "does not exist") {
		return ErrPipelineUnavailable
	}
	return err
}

// NetworkRepository reads the AI service's F5 detection tables, overlaying this
// backend's own cis_network_reviews.
//
// It exposes no write method for any AI-owned table. Every mutation here
// targets a cis_* table, and the review overlay is the only reason this
// repository writes at all.
type NetworkRepository struct {
	db *gorm.DB
}

// NewNetworkRepository constructs a NetworkRepository.
func NewNetworkRepository(db *gorm.DB) *NetworkRepository {
	return &NetworkRepository{db: db}
}

// NetworkRow is a detected network joined with its review-status overlay, its
// primary claim, and the run-level facts the list has to show.
type NetworkRow struct {
	models.AICoordinatedNetwork `gorm:"embedded"`

	// ReviewStatus is COALESCE(review.status, 'unreviewed'). Orthogonal to
	// ConfidenceBand: the band is computed by PRD 10.6.2, the status is set by
	// a human under US52, and PRD 10.10 forbids expressing any visibility rule
	// as a disjunction across the two.
	ReviewStatus string     `gorm:"column:review_status"`
	ReviewReason *string    `gorm:"column:review_reason"`
	ReviewedBy   *uuid.UUID `gorm:"column:reviewed_by"`
	ReviewedAt   *time.Time `gorm:"column:reviewed_at"`

	// Primary claim, resolved through network_claim_link.is_primary_claim.
	PrimaryClaimID        *uuid.UUID `gorm:"column:primary_claim_id"`
	PrimaryClaimStatement *string    `gorm:"column:primary_claim_statement"`
	PrimaryTopicID        *uuid.UUID `gorm:"column:primary_topic_id"`
	PrimaryTopicName      *string    `gorm:"column:primary_topic_name"`
	OverlapRatio          *float64   `gorm:"column:overlap_ratio"`
	AnchoringShare        *float64   `gorm:"column:anchoring_share"`
	ClaimClusterPostCnt   *int       `gorm:"column:claim_cluster_post_count"`
	PassedRelevanceGate   *bool      `gorm:"column:passed_relevance_gate"`

	// Run-level facts. Truncation and signal unavailability cap every network
	// in a run at Medium (PRD 10.6.3 rule 4), and PRD 10.5.1 requires the
	// truncation flag on the detail page rather than only in storage.
	RunTruncated          bool              `gorm:"column:run_truncated"`
	RunSignalsUnavailable models.StringList `gorm:"column:run_signals_unavailable"`
	RunCandidatesCount    int               `gorm:"column:run_candidates_count"`
	RunWindowStart        time.Time         `gorm:"column:run_window_start"`
	RunWindowEnd          time.Time         `gorm:"column:run_window_end"`
	RunCompletedAt        *time.Time        `gorm:"column:run_completed_at"`
	RunTriggerSource      string            `gorm:"column:run_trigger_source"`

	// RecurrenceCount is how many times this fingerprint has been seen,
	// counting the current detection. Drives US46's "Seen 3x since 12 Jun".
	RecurrenceCount int        `gorm:"column:recurrence_count"`
	FirstSeenAt     *time.Time `gorm:"column:first_seen_at"`
}

// NetworkSort keys (US48).
const (
	NetworkSortScore       = "score"
	NetworkSortDetected    = "detected_at"
	NetworkSortAccounts    = "accounts"
	NetworkSortPosts       = "posts"
	NetworkSortRecurrences = "recurrences"
)

// NetworkFilter describes the F5 list query (US43-US48).
type NetworkFilter struct {
	// ReviewStatus filters on the overlaid status. Empty or "all" means all.
	ReviewStatus string
	// ConfidenceBands restricts to specific bands. Empty means the default
	// surfaceable set — see IncludeLowConfidence.
	ConfidenceBands []string
	// IncludeLowConfidence is US43's "Show low-confidence networks" toggle,
	// off by default. It only ever widens the F5 list; it has no effect on F1,
	// which has no such toggle (PRD 10.6.3 rule 5).
	IncludeLowConfidence bool

	ClaimIDs  []uuid.UUID
	TopicIDs  []uuid.UUID
	PolicyIDs []uuid.UUID
	RunID     *uuid.UUID

	DetectedFrom *time.Time
	DetectedTo   *time.Time

	// Search matches the linked claim statement, the network label, and member
	// account handles, including partial handles (US47).
	Search string

	SortBy string
	Limit  int
	Offset int
}

// baseQuery builds the shared FROM/JOIN/WHERE for network listing.
//
// Three things happen here that are easy to get wrong elsewhere:
//
//  1. The review overlay is LEFT JOINed and resolved in SQL, so filtering by
//     status pages correctly. Same pattern as claims.
//  2. Suppressed networks are excluded unconditionally. PRD 10.6.3 rule 3
//     suppresses an allowlisted network "entirely" — there is no surface and no
//     toggle that reveals it, so the exclusion belongs in the base query rather
//     than in each caller.
//  3. Clusters below N_min are likewise never surfaced (rule 1). The pipeline
//     already applies the retention filter, but a cluster whose membership was
//     later reduced by allowlisting could fall below it, so it is re-asserted.
func (r *NetworkRepository) baseQuery(ctx context.Context, f NetworkFilter) *gorm.DB {
	q := r.db.WithContext(ctx).
		Table("coordinated_network AS n").
		Joins("LEFT JOIN cis_network_reviews AS rev ON rev.network_id = n.network_id").
		Joins("JOIN detection_run AS run ON run.run_id = n.run_id").
		Joins("LEFT JOIN network_claim_link AS pcl ON pcl.network_id = n.network_id AND pcl.is_primary_claim = true").
		Joins("LEFT JOIN claims AS pc ON pc.id = pcl.claim_id").
		Joins("LEFT JOIN topics AS pt ON pt.id = pc.topic_id").
		Where("n.allowlist_suppressed = false")

	if f.ReviewStatus != "" && f.ReviewStatus != "all" {
		q = q.Where("COALESCE(rev.status, ?) = ?", models.NetworkStatusUnreviewed, f.ReviewStatus)
	}

	switch {
	case len(f.ConfidenceBands) > 0:
		q = q.Where("n.confidence_band IN ?", f.ConfidenceBands)
	case !f.IncludeLowConfidence:
		// US43: the list shows Medium and High by default. Low is de-emphasised
		// and hidden behind an explicit toggle (PRD 10.6.3 rule 2).
		q = q.Where("n.confidence_band IN ?", models.SurfaceableConfidenceBands)
	}

	if len(f.ClaimIDs) > 0 {
		q = q.Where(
			"EXISTS (SELECT 1 FROM network_claim_link l WHERE l.network_id = n.network_id AND l.claim_id IN ? AND l.passed_relevance_gate = true)",
			f.ClaimIDs,
		)
	}
	if len(f.TopicIDs) > 0 {
		q = q.Where(
			"EXISTS (SELECT 1 FROM network_claim_link l JOIN claims lc ON lc.id = l.claim_id "+
				"WHERE l.network_id = n.network_id AND lc.topic_id IN ?)",
			f.TopicIDs,
		)
	}
	if len(f.PolicyIDs) > 0 {
		// US45: filtering by policy resolves transitively —
		// policy -> linked claims -> networks amplifying those claims. Both
		// claim/policy relations are followed, the many-to-many join for
		// Existing claims and the direct column for Synthetic ones, matching
		// ClaimFilter.PolicyIDs.
		q = q.Where(
			"EXISTS (SELECT 1 FROM network_claim_link l JOIN claims lc ON lc.id = l.claim_id "+
				"WHERE l.network_id = n.network_id AND (lc.policy_id IN ? OR EXISTS "+
				"(SELECT 1 FROM claim_policies cp WHERE cp.claim_id = lc.id AND cp.policy_id IN ?)))",
			f.PolicyIDs, f.PolicyIDs,
		)
	}
	if f.RunID != nil {
		q = q.Where("n.run_id = ?", *f.RunID)
	}
	if f.DetectedFrom != nil {
		q = q.Where("n.created_at >= ?", *f.DetectedFrom)
	}
	if f.DetectedTo != nil {
		q = q.Where("n.created_at <= ?", *f.DetectedTo)
	}

	if s := strings.TrimSpace(f.Search); s != "" {
		pattern := "%" + escapeLike(s) + "%"
		// US47 searches three different things, one of which lives two joins
		// away. The handle arm is an EXISTS rather than a join so a network
		// with 300 matching members still yields one row.
		q = q.Where(
			r.db.Where("n.label ILIKE ?", pattern).
				Or("pc.claim_statement ILIKE ?", pattern).
				Or("EXISTS (SELECT 1 FROM network_account na JOIN account a ON a.account_id = na.account_id "+
					"WHERE na.network_id = n.network_id AND a.handle ILIKE ?)", pattern),
		)
	}

	return q
}

const networkSelectColumns = `n.*,
	COALESCE(rev.status, ?) AS review_status,
	rev.reason AS review_reason,
	rev.reviewed_by AS reviewed_by,
	rev.reviewed_at AS reviewed_at,
	pcl.claim_id AS primary_claim_id,
	pc.claim_statement AS primary_claim_statement,
	pc.topic_id AS primary_topic_id,
	pt.name AS primary_topic_name,
	pcl.overlap_ratio AS overlap_ratio,
	pcl.anchoring_share AS anchoring_share,
	pcl.claim_cluster_post_count AS claim_cluster_post_count,
	pcl.passed_relevance_gate AS passed_relevance_gate,
	run.truncated_bool AS run_truncated,
	run.signals_unavailable AS run_signals_unavailable,
	run.candidates_count AS run_candidates_count,
	run.window_start AS run_window_start,
	run.window_end AS run_window_end,
	run.completed_at AS run_completed_at,
	run.trigger_source AS run_trigger_source,
	(SELECT COUNT(*) FROM coordinated_network sib WHERE sib.fingerprint_hash = n.fingerprint_hash) AS recurrence_count,
	(SELECT MIN(sib.created_at) FROM coordinated_network sib WHERE sib.fingerprint_hash = n.fingerprint_hash) AS first_seen_at`

// ListNetworks returns a page of networks matching the filter (US43-US48).
func (r *NetworkRepository) ListNetworks(ctx context.Context, f NetworkFilter) ([]NetworkRow, error) {
	var rows []NetworkRow

	q := r.baseQuery(ctx, f).
		Select(networkSelectColumns, models.NetworkStatusUnreviewed).
		Order(networkOrderClause(f.SortBy))

	if f.Limit > 0 {
		q = q.Limit(f.Limit)
	}
	if f.Offset > 0 {
		q = q.Offset(f.Offset)
	}

	if err := q.Scan(&rows).Error; err != nil {
		return nil, classify(err)
	}
	return rows, nil
}

// CountNetworks returns the total matching the filter, ignoring paging.
func (r *NetworkRepository) CountNetworks(ctx context.Context, f NetworkFilter) (int64, error) {
	var total int64
	err := r.baseQuery(ctx, f).Select("COUNT(DISTINCT n.network_id)").Scan(&total).Error
	return total, classify(err)
}

// FindNetworkByID loads one network with every overlay and run-level fact.
//
// Deliberately does NOT apply the confidence-band or suppression filters that
// baseQuery does. A suppressed or Low-confidence network must not be *listed*,
// but an analyst who followed a direct link — from the audit log, from a report,
// from a bookmark — is entitled to see why it was suppressed rather than a 404
// that looks like data loss. The service decides what to do with that.
func (r *NetworkRepository) FindNetworkByID(ctx context.Context, id uuid.UUID) (*NetworkRow, error) {
	var row NetworkRow
	err := r.db.WithContext(ctx).
		Table("coordinated_network AS n").
		Joins("LEFT JOIN cis_network_reviews AS rev ON rev.network_id = n.network_id").
		Joins("JOIN detection_run AS run ON run.run_id = n.run_id").
		Joins("LEFT JOIN network_claim_link AS pcl ON pcl.network_id = n.network_id AND pcl.is_primary_claim = true").
		Joins("LEFT JOIN claims AS pc ON pc.id = pcl.claim_id").
		Joins("LEFT JOIN topics AS pt ON pt.id = pc.topic_id").
		Select(networkSelectColumns, models.NetworkStatusUnreviewed).
		Where("n.network_id = ?", id).
		Limit(1).
		Scan(&row).Error
	if err != nil {
		return nil, classify(err)
	}
	if row.ID == uuid.Nil {
		return nil, ErrNotFound
	}
	return &row, nil
}

// networkOrderClause maps US48's sort control onto a deterministic ORDER BY.
//
// Every branch ends with n.network_id so paging is stable when the leading key
// ties, which it frequently does on account and post counts.
func networkOrderClause(sortBy string) string {
	switch sortBy {
	case NetworkSortDetected:
		return "n.created_at DESC, n.network_id DESC"
	case NetworkSortAccounts:
		return "n.account_count DESC, n.coordination_score DESC, n.network_id DESC"
	case NetworkSortPosts:
		return "n.post_count DESC, n.coordination_score DESC, n.network_id DESC"
	case NetworkSortRecurrences:
		return "recurrence_count DESC, n.coordination_score DESC, n.network_id DESC"
	default:
		return "n.coordination_score DESC, n.created_at DESC, n.network_id DESC"
	}
}

// ClaimLinkRow is one network<->claim link with the claim's own label.
type ClaimLinkRow struct {
	models.AINetworkClaimLink `gorm:"embedded"`
	ClaimStatement            string     `gorm:"column:claim_statement"`
	ClaimType                 string     `gorm:"column:claim_type"`
	TopicID                   *uuid.UUID `gorm:"column:topic_id"`
	TopicName                 *string    `gorm:"column:topic_name"`
}

// ListClaimLinks returns every claim a network is linked to, primary first
// (US49, US50's relevance block).
func (r *NetworkRepository) ListClaimLinks(ctx context.Context, networkID uuid.UUID) ([]ClaimLinkRow, error) {
	var rows []ClaimLinkRow
	err := r.db.WithContext(ctx).
		Table("network_claim_link AS l").
		Joins("JOIN claims AS c ON c.id = l.claim_id").
		Joins("LEFT JOIN topics AS t ON t.id = c.topic_id").
		Select("l.*, c.claim_statement, c.claim_type, c.topic_id, t.name AS topic_name").
		Where("l.network_id = ?", networkID).
		Order("l.is_primary_claim DESC, l.overlap_ratio DESC").
		Scan(&rows).Error
	return rows, classify(err)
}

// AncestorLink is one prior detection in a network's recurrence chain.
//
// PRD 10.5.7 and 10.5.1 together: a recurrence inherits a network's history but
// NOT its relevance, and both the detail page and the report must state the
// current primary claim AND the prior anchoring claims. "This same set of
// accounts previously amplified claims X and Y" is the sentence that makes a
// platform referral actionable, so the chain has to be walked and each
// ancestor's own primary claim resolved.
type AncestorLink struct {
	NetworkID      uuid.UUID  `gorm:"column:network_id"`
	Label          string     `gorm:"column:label"`
	DetectedAt     time.Time  `gorm:"column:detected_at"`
	ConfidenceBand string     `gorm:"column:confidence_band"`
	Score          float64    `gorm:"column:coordination_score"`
	ClaimID        *uuid.UUID `gorm:"column:claim_id"`
	ClaimStatement *string    `gorm:"column:claim_statement"`
}

// ListRecurrenceChain walks parent_network_id back from a network and returns
// each ancestor with its own primary claim, oldest first.
//
// A recursive CTE rather than a loop of queries: the chain is short in practice
// but its length is unbounded in principle, and one round trip is one round trip.
// The depth guard is a cycle brake, not a business rule — parent_network_id is
// written by the pipeline and a bug there should degrade to a truncated history
// rather than to a hung request.
func (r *NetworkRepository) ListRecurrenceChain(ctx context.Context, networkID uuid.UUID) ([]AncestorLink, error) {
	const query = `
WITH RECURSIVE chain AS (
	SELECT n.network_id, n.parent_network_id, 0 AS depth
	FROM coordinated_network n
	WHERE n.network_id = ?
	UNION ALL
	SELECT p.network_id, p.parent_network_id, chain.depth + 1
	FROM coordinated_network p
	JOIN chain ON chain.parent_network_id = p.network_id
	WHERE chain.depth < 50
)
SELECT n.network_id,
       n.label,
       n.created_at AS detected_at,
       n.confidence_band,
       n.coordination_score,
       l.claim_id,
       c.claim_statement
FROM chain
JOIN coordinated_network n ON n.network_id = chain.network_id
LEFT JOIN network_claim_link l ON l.network_id = n.network_id AND l.is_primary_claim = true
LEFT JOIN claims c ON c.id = l.claim_id
WHERE chain.depth > 0
ORDER BY n.created_at ASC`

	var rows []AncestorLink
	err := r.db.WithContext(ctx).Raw(query, networkID).Scan(&rows).Error
	return rows, classify(err)
}

// FindRun loads one detection run (US62's run history, US49's header).
func (r *NetworkRepository) FindRun(ctx context.Context, runID uuid.UUID) (*models.AIDetectionRun, error) {
	var run models.AIDetectionRun
	err := r.db.WithContext(ctx).Table("detection_run").Where("run_id = ?", runID).Limit(1).Scan(&run).Error
	if err != nil {
		return nil, classify(err)
	}
	if run.ID == uuid.Nil {
		return nil, ErrNotFound
	}
	return &run, nil
}

// RunFilter narrows the detection-run history.
type RunFilter struct {
	Status        string
	TriggerSource string
	OnlyTruncated bool
	From          *time.Time
	To            *time.Time
	Limit         int
	Offset        int
}

// RunRow is a detection run with the count of networks it produced.
type RunRow struct {
	models.AIDetectionRun `gorm:"embedded"`
	NetworkCount          int64 `gorm:"column:network_count"`
	OfftopicCount         int64 `gorm:"column:offtopic_count"`
}

// ListRuns returns the run history.
//
// Truncation and signal unavailability are RUN-level facts that cap confidence
// for every network in that run (PRD 10.6.3 rule 4), so "why is everything
// Medium this week?" is a question about runs, not about networks, and needs a
// surface of its own.
func (r *NetworkRepository) ListRuns(ctx context.Context, f RunFilter) ([]RunRow, int64, error) {
	q := r.db.WithContext(ctx).Table("detection_run AS run")

	if f.Status != "" {
		q = q.Where("run.status = ?", f.Status)
	}
	if f.TriggerSource != "" {
		q = q.Where("run.trigger_source = ?", f.TriggerSource)
	}
	if f.OnlyTruncated {
		q = q.Where("run.truncated_bool = true")
	}
	if f.From != nil {
		q = q.Where("run.started_at >= ?", *f.From)
	}
	if f.To != nil {
		q = q.Where("run.started_at <= ?", *f.To)
	}

	var total int64
	if err := q.Session(&gorm.Session{}).Select("COUNT(run.run_id)").Scan(&total).Error; err != nil {
		return nil, 0, classify(err)
	}

	var rows []RunRow
	err := q.Session(&gorm.Session{}).
		Select("run.*, " +
			"(SELECT COUNT(*) FROM coordinated_network n WHERE n.run_id = run.run_id) AS network_count, " +
			"(SELECT COUNT(*) FROM offtopic_cluster o WHERE o.run_id = run.run_id) AS offtopic_count").
		Order("run.started_at DESC, run.run_id DESC").
		Limit(f.Limit).
		Offset(f.Offset).
		Scan(&rows).Error
	if err != nil {
		return nil, 0, classify(err)
	}
	return rows, total, nil
}

// LastRunStartedAt returns when the most recent run with the given trigger
// source started, or nil when there has never been one.
//
// The scheduled sweep's cadence is a detector setting (1-24 h, PRD 10.5.8), not
// a cron expression, so the job cannot express it as a schedule: cron is fixed
// at boot and the setting changes at runtime. The tick therefore fires often
// and asks this question first. A pending or running row counts — two sweeps
// overlapping is worse than one sweep late.
func (r *NetworkRepository) LastRunStartedAt(ctx context.Context, triggerSource string) (*time.Time, error) {
	var startedAt *time.Time
	err := r.db.WithContext(ctx).
		Table("detection_run").
		Select("MAX(started_at)").
		Where("trigger_source = ?", triggerSource).
		Scan(&startedAt).Error
	if err != nil {
		return nil, classify(err)
	}
	return startedAt, nil
}

// NetworkAccountRow is one member (or comparison) account with its behavioural
// metrics, for the US55 annex and the US51 graph.
type NetworkAccountRow struct {
	models.AINetworkAccount `gorm:"embedded"`
	Handle                  string     `gorm:"column:handle"`
	Platform                string     `gorm:"column:platform"`
	PlatformAccountID       string     `gorm:"column:platform_account_id"`
	CreatedAtPlatform       *time.Time `gorm:"column:created_at_platform"`
	// Allowlisted marks a member the team has since declared legitimate. Shown
	// so an analyst reading the annex can see which rows no longer count
	// against the network, without the row silently disappearing.
	Allowlisted bool `gorm:"column:allowlisted"`
}

// AccountSort keys for the US55 annex.
const (
	AccountSortHandle       = "handle"
	AccountSortPosts        = "posts_in_cluster"
	AccountSortDuplication  = "duplication_rate"
	AccountSortCentrality   = "centrality"
	AccountSortCreated      = "created_at_platform"
	AccountSortCircadian    = "circadian_coverage"
	AccountSortInterpostGap = "median_interpost"
)

// ListNetworkAccounts returns a page of a network's accounts (US55).
//
// role selects members, comparison accounts, or (empty) both. The comparison
// set is what US51 renders "in a visually distinct style, for contrast" and
// what PRD 10.8 item 5 counts in the report; it is stored on the same table
// under a membership role rather than in a second table.
func (r *NetworkRepository) ListNetworkAccounts(
	ctx context.Context, networkID uuid.UUID, role, sortBy, search string, limit, offset int,
) ([]NetworkAccountRow, int64, error) {
	q := r.db.WithContext(ctx).
		Table("network_account AS na").
		Joins("JOIN account AS a ON a.account_id = na.account_id").
		Where("na.network_id = ?", networkID)

	if role != "" {
		q = q.Where("na.membership_role = ?", role)
	}
	if s := strings.TrimSpace(search); s != "" {
		q = q.Where("a.handle ILIKE ?", "%"+escapeLike(s)+"%")
	}

	var total int64
	if err := q.Session(&gorm.Session{}).Select("COUNT(*)").Scan(&total).Error; err != nil {
		return nil, 0, classify(err)
	}

	q = q.Session(&gorm.Session{}).
		Select("na.*, a.handle, a.platform, a.platform_account_id, a.created_at_platform, " +
			"EXISTS (SELECT 1 FROM cis_coordination_allowlist w WHERE w.removed_at IS NULL " +
			"AND w.platform = a.platform AND w.platform_account_id = a.platform_account_id) AS allowlisted").
		Order(accountOrderClause(sortBy))
	if limit > 0 {
		q = q.Limit(limit).Offset(offset)
	}

	var rows []NetworkAccountRow
	if err := q.Scan(&rows).Error; err != nil {
		return nil, 0, classify(err)
	}
	return rows, total, nil
}

func accountOrderClause(sortBy string) string {
	switch sortBy {
	case AccountSortHandle:
		return "a.handle ASC, na.account_id ASC"
	case AccountSortDuplication:
		return "na.duplication_rate DESC, na.account_id ASC"
	case AccountSortCreated:
		return "a.created_at_platform ASC NULLS LAST, na.account_id ASC"
	case AccountSortCircadian:
		return "na.circadian_coverage DESC, na.account_id ASC"
	case AccountSortInterpostGap:
		return "na.median_interpost_interval_seconds ASC NULLS LAST, na.account_id ASC"
	case AccountSortPosts:
		return "na.posts_in_cluster DESC, na.account_id ASC"
	default:
		// Centrality first: the annex's job is to let an analyst inspect
		// membership, and the hubs are what a referral is built around.
		return "na.eigenvector_centrality DESC, na.degree_centrality DESC, na.account_id ASC"
	}
}

// FindNetworkAccount loads one membership row for the US55 drawer.
func (r *NetworkRepository) FindNetworkAccount(ctx context.Context, networkID, accountID uuid.UUID) (*NetworkAccountRow, error) {
	var row NetworkAccountRow
	err := r.db.WithContext(ctx).
		Table("network_account AS na").
		Joins("JOIN account AS a ON a.account_id = na.account_id").
		Select("na.*, a.handle, a.platform, a.platform_account_id, a.created_at_platform, "+
			"EXISTS (SELECT 1 FROM cis_coordination_allowlist w WHERE w.removed_at IS NULL "+
			"AND w.platform = a.platform AND w.platform_account_id = a.platform_account_id) AS allowlisted").
		Where("na.network_id = ? AND na.account_id = ?", networkID, accountID).
		Limit(1).
		Scan(&row).Error
	if err != nil {
		return nil, classify(err)
	}
	if row.AccountID == uuid.Nil {
		return nil, ErrNotFound
	}
	return &row, nil
}

// EdgeRow is one retained edge with both endpoints' handles.
type EdgeRow struct {
	models.AINetworkEdge `gorm:"embedded"`
	HandleA              string `gorm:"column:handle_a"`
	HandleB              string `gorm:"column:handle_b"`
}

// ListEdges returns a network's retained edges with their per-family
// decomposition (US51's hover detail).
func (r *NetworkRepository) ListEdges(ctx context.Context, networkID uuid.UUID) ([]EdgeRow, error) {
	var rows []EdgeRow
	err := r.db.WithContext(ctx).
		Table("network_edge AS e").
		Joins("JOIN account AS aa ON aa.account_id = e.account_a").
		Joins("JOIN account AS ab ON ab.account_id = e.account_b").
		Select("e.*, aa.handle AS handle_a, ab.handle AS handle_b").
		Where("e.network_id = ?", networkID).
		Order("e.w_total DESC").
		Scan(&rows).Error
	return rows, classify(err)
}

// ListEdgesForAccount returns the specific edges that connected one account to
// its network.
//
// This is the query behind US55's hard rule: "No account may appear in a
// network without a viewable reason." Every edge carries its per-family weights,
// so "why was this account included?" always resolves to a concrete answer.
func (r *NetworkRepository) ListEdgesForAccount(ctx context.Context, networkID, accountID uuid.UUID) ([]EdgeRow, error) {
	var rows []EdgeRow
	err := r.db.WithContext(ctx).
		Table("network_edge AS e").
		Joins("JOIN account AS aa ON aa.account_id = e.account_a").
		Joins("JOIN account AS ab ON ab.account_id = e.account_b").
		Select("e.*, aa.handle AS handle_a, ab.handle AS handle_b").
		Where("e.network_id = ? AND (e.account_a = ? OR e.account_b = ?)", networkID, accountID, accountID).
		Order("e.w_total DESC").
		Scan(&rows).Error
	return rows, classify(err)
}

// ListBurstBins returns the US53 timeline.
func (r *NetworkRepository) ListBurstBins(ctx context.Context, networkID uuid.UUID) ([]models.AINetworkBurstBin, error) {
	var rows []models.AINetworkBurstBin
	err := r.db.WithContext(ctx).
		Table("network_burst_bin").
		Where("network_id = ?", networkID).
		Order("bin_start ASC").
		Scan(&rows).Error
	return rows, classify(err)
}

// EvidencePostRow is one snapshotted post with its author's handle.
type EvidencePostRow struct {
	models.AINetworkEvidencePost `gorm:"embedded"`
	Handle                       string `gorm:"column:handle"`
	Platform                     string `gorm:"column:platform"`
}

// ListEvidencePosts returns a network's snapshotted posts (US54, US60's
// posts.csv).
//
// Ordered so that each duplicate group's canonical post leads its variants,
// which is the shape US54 renders and the shape PRD 10.8 item 6 prints.
func (r *NetworkRepository) ListEvidencePosts(ctx context.Context, networkID uuid.UUID, groupID *uuid.UUID) ([]EvidencePostRow, error) {
	q := r.db.WithContext(ctx).
		Table("network_evidence_post AS p").
		Joins("LEFT JOIN account AS a ON a.account_id = p.account_id").
		Select("p.*, a.handle, a.platform").
		Where("p.network_id = ?", networkID)

	if groupID != nil {
		q = q.Where("p.duplicate_group_id = ?", *groupID)
	}

	var rows []EvidencePostRow
	err := q.Order("p.duplicate_group_id NULLS LAST, p.is_canonical DESC, p.posted_at ASC").Scan(&rows).Error
	return rows, classify(err)
}

// FindSnapshot loads the evidence snapshot header for a network's chain of
// custody (PRD 10.8 item 10).
func (r *NetworkRepository) FindSnapshot(ctx context.Context, networkID uuid.UUID) (*models.AIEvidenceSnapshot, error) {
	var snap models.AIEvidenceSnapshot
	err := r.db.WithContext(ctx).
		Table("evidence_snapshot").
		Where("network_id = ?", networkID).
		Order("created_at DESC").
		Limit(1).
		Scan(&snap).Error
	if err != nil {
		return nil, classify(err)
	}
	if snap.ID == uuid.Nil {
		return nil, ErrNotFound
	}
	return &snap, nil
}

// OfftopicFilter narrows the read-only off-topic cluster review (US62).
type OfftopicFilter struct {
	RunID      *uuid.UUID
	ClaimID    *uuid.UUID
	FailedTest string
	From       *time.Time
	To         *time.Time
	Limit      int
	Offset     int
}

// OfftopicRow is one rejected-but-real cluster with its claim label.
type OfftopicRow struct {
	models.AIOfftopicCluster `gorm:"embedded"`
	ClaimStatement           *string `gorm:"column:claim_statement"`
}

// ListOfftopicClusters returns the recalibration view (US62).
//
// These clusters are never surfaced in the network list and never exported in a
// report — PRD 10.5.1a is explicit that they are not the city's problem and
// must not appear in a climate report. This one read-only admin surface is
// their entire reason for being retained: a rising off-topic rate is the signal
// that omega_min or the candidate scope needs recalibration.
func (r *NetworkRepository) ListOfftopicClusters(ctx context.Context, f OfftopicFilter) ([]OfftopicRow, int64, error) {
	q := r.db.WithContext(ctx).
		Table("offtopic_cluster AS o").
		Joins("LEFT JOIN claims AS c ON c.id = o.claim_id")

	if f.RunID != nil {
		q = q.Where("o.run_id = ?", *f.RunID)
	}
	if f.ClaimID != nil {
		q = q.Where("o.claim_id = ?", *f.ClaimID)
	}
	if f.FailedTest != "" {
		q = q.Where("o.failed_test = ?", f.FailedTest)
	}
	if f.From != nil {
		q = q.Where("o.created_at >= ?", *f.From)
	}
	if f.To != nil {
		q = q.Where("o.created_at <= ?", *f.To)
	}

	var total int64
	if err := q.Session(&gorm.Session{}).Select("COUNT(o.cluster_id)").Scan(&total).Error; err != nil {
		return nil, 0, classify(err)
	}

	var rows []OfftopicRow
	err := q.Session(&gorm.Session{}).
		Select("o.*, c.claim_statement").
		Order("o.created_at DESC, o.cluster_id DESC").
		Limit(f.Limit).
		Offset(f.Offset).
		Scan(&rows).Error
	if err != nil {
		return nil, 0, classify(err)
	}
	return rows, total, nil
}

// OfftopicRateRow is one run's off-topic rate, the aggregate US62 asks for.
type OfftopicRateRow struct {
	RunID         uuid.UUID `gorm:"column:run_id"`
	StartedAt     time.Time `gorm:"column:started_at"`
	SurfacedCount int64     `gorm:"column:surfaced_count"`
	OfftopicCount int64     `gorm:"column:offtopic_count"`
	FailedTests   string    `gorm:"column:failed_tests"`
}

// OfftopicRates returns per-run surfaced-vs-rejected counts so an admin can see
// whether the relevance gate is set too loose or too tight (US62).
func (r *NetworkRepository) OfftopicRates(ctx context.Context, limit int) ([]OfftopicRateRow, error) {
	const query = `
SELECT run.run_id,
       run.started_at,
       (SELECT COUNT(*) FROM coordinated_network n WHERE n.run_id = run.run_id) AS surfaced_count,
       (SELECT COUNT(*) FROM offtopic_cluster o WHERE o.run_id = run.run_id) AS offtopic_count,
       COALESCE((SELECT string_agg(DISTINCT o2.failed_test, ',')
                 FROM offtopic_cluster o2 WHERE o2.run_id = run.run_id), '') AS failed_tests
FROM detection_run run
ORDER BY run.started_at DESC
LIMIT ?`

	var rows []OfftopicRateRow
	err := r.db.WithContext(ctx).Raw(query, limit).Scan(&rows).Error
	return rows, classify(err)
}

// NetworkBadge is the minimal per-claim network summary the F1 claim card and
// claim detail page need (US61).
type NetworkBadge struct {
	ClaimID           uuid.UUID `gorm:"column:claim_id"`
	NetworkID         uuid.UUID `gorm:"column:network_id"`
	Label             string    `gorm:"column:label"`
	CoordinationScore float64   `gorm:"column:coordination_score"`
	ConfidenceBand    string    `gorm:"column:confidence_band"`
	ReviewStatus      string    `gorm:"column:review_status"`
	AccountCount      int       `gorm:"column:account_count"`
	// OtherCount is how many further networks also qualify for this claim.
	// US61: "Where more than one network qualifies, show the highest-scoring
	// one and a count of the others."
	OtherCount int `gorm:"column:other_count"`
}

// BadgesForClaims resolves the US61 cross-link for a page of claims in one
// grouped query.
//
// # The gate, in full
//
// US61 states four conditions and every one of them is in the WHERE clause
// below, because a gate assembled from fewer clauses is the failure mode this
// endpoint exists to avoid:
//
//  1. network_claim_link.passed_relevance_gate = true. A run anchored to a
//     claim does not make the clusters it finds *about* that claim.
//  2. confidence_band IN (medium, high). F1 has no low-confidence toggle, so a
//     Low network has no surface here at all.
//  3. review status is NOT 'dismissed_false_positive'. Without this clause the
//     claim page badges a network the team has already examined and concluded
//     was organic — a government telling its own analysts that residents it
//     cleared are a coordinated network.
//  4. not suppressed under PRD 10.6.3. Rule 5 makes suppression bind every
//     surface: "a network invisible in F5 must not be reachable through F1."
//     The >= 60% allowlisted case (rule 3) is the one that bites, because such
//     a network is by the team's own declaration civil society coordinating
//     openly.
//
// PRD 10.10 forbids expressing this as a disjunction across band and review
// status — they are orthogonal axes. It is an AND of all four, and the two
// axes appear as two separate conditions rather than as one combined test.
//
// # Why one query
//
// Called with a whole page of claim ids from three different card paths, so an
// N+1 here would be a per-card round trip. DISTINCT ON picks the highest-scoring
// qualifying network per claim, and the correlated count supplies US61's "and
// N others" in the same pass.
func (r *NetworkRepository) BadgesForClaims(ctx context.Context, claimIDs []uuid.UUID) (map[uuid.UUID]NetworkBadge, error) {
	out := make(map[uuid.UUID]NetworkBadge, len(claimIDs))
	if len(claimIDs) == 0 {
		return out, nil
	}

	const query = `
SELECT DISTINCT ON (l.claim_id)
       l.claim_id,
       n.network_id,
       n.label,
       n.coordination_score,
       n.confidence_band,
       COALESCE(rev.status, ?) AS review_status,
       n.account_count,
       (SELECT COUNT(*) - 1
          FROM network_claim_link l2
          JOIN coordinated_network n2 ON n2.network_id = l2.network_id
          LEFT JOIN cis_network_reviews rev2 ON rev2.network_id = n2.network_id
         WHERE l2.claim_id = l.claim_id
           AND l2.passed_relevance_gate = true
           AND n2.confidence_band IN ?
           AND COALESCE(rev2.status, ?) <> ?
           AND n2.allowlist_suppressed = false) AS other_count
FROM network_claim_link l
JOIN coordinated_network n ON n.network_id = l.network_id
LEFT JOIN cis_network_reviews rev ON rev.network_id = n.network_id
WHERE l.claim_id IN ?
  AND l.passed_relevance_gate = true
  AND n.confidence_band IN ?
  AND COALESCE(rev.status, ?) <> ?
  AND n.allowlist_suppressed = false
ORDER BY l.claim_id, n.coordination_score DESC, n.created_at DESC, n.network_id DESC`

	var rows []NetworkBadge
	err := r.db.WithContext(ctx).Raw(query,
		models.NetworkStatusUnreviewed,
		models.SurfaceableConfidenceBands, models.NetworkStatusUnreviewed, models.NetworkStatusDismissedFP,
		claimIDs,
		models.SurfaceableConfidenceBands, models.NetworkStatusUnreviewed, models.NetworkStatusDismissedFP,
	).Scan(&rows).Error
	if err != nil {
		// A claim page must not 500 because the detector was never deployed.
		// US61 says the indicator is absent when nothing qualifies, and "the
		// pipeline does not exist" is a legitimate instance of nothing
		// qualifying — so F1 degrades to no badge rather than to an error.
		if errors.Is(classify(err), ErrPipelineUnavailable) {
			return out, nil
		}
		return nil, err
	}

	for _, row := range rows {
		out[row.ClaimID] = row
	}
	return out, nil
}

// CountNetworksByStatus returns the per-tab counts for the US44 status filter.
func (r *NetworkRepository) CountNetworksByStatus(ctx context.Context, f NetworkFilter) (map[string]int64, error) {
	type statusCount struct {
		Status string `gorm:"column:status"`
		Total  int64  `gorm:"column:total"`
	}

	// Status is cleared so every tab's count reflects the other filters but not
	// the tab currently selected, which is what a tab bar showing counts means.
	f.ReviewStatus = ""

	var rows []statusCount
	err := r.baseQuery(ctx, f).
		Select("COALESCE(rev.status, ?) AS status, COUNT(DISTINCT n.network_id) AS total", models.NetworkStatusUnreviewed).
		Group("COALESCE(rev.status, '" + models.NetworkStatusUnreviewed + "')").
		Scan(&rows).Error
	if err != nil {
		return nil, classify(err)
	}

	out := make(map[string]int64, len(models.ValidNetworkReviewStatuses))
	for _, s := range models.ValidNetworkReviewStatuses {
		out[s] = 0
	}
	for _, row := range rows {
		out[row.Status] = row.Total
	}
	return out, nil
}

// NetworkExists reports whether a network id is present.
func (r *NetworkRepository) NetworkExists(ctx context.Context, id uuid.UUID) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Table("coordinated_network").Where("network_id = ?", id).Count(&count).Error
	if err != nil {
		return false, classify(err)
	}
	return count > 0, nil
}

// PlatformAccountKey identifies an account the way the allowlist does.
type PlatformAccountKey struct {
	Platform          string `gorm:"column:platform" json:"platform"`
	PlatformAccountID string `gorm:"column:platform_account_id" json:"platform_account_id"`
	Handle            string `gorm:"column:handle" json:"handle"`
}

// ListMemberKeys returns the platform identities of a network's members, for
// US56's "mark the whole network as legitimate coordination".
func (r *NetworkRepository) ListMemberKeys(ctx context.Context, networkID uuid.UUID) ([]PlatformAccountKey, error) {
	var rows []PlatformAccountKey
	err := r.db.WithContext(ctx).
		Table("network_account AS na").
		Joins("JOIN account AS a ON a.account_id = na.account_id").
		Select("a.platform, a.platform_account_id, a.handle").
		Where("na.network_id = ? AND na.membership_role = ?", networkID, models.MembershipMember).
		Scan(&rows).Error
	return rows, classify(err)
}

// FindAccountKey resolves one account's platform identity.
func (r *NetworkRepository) FindAccountKey(ctx context.Context, accountID uuid.UUID) (*PlatformAccountKey, error) {
	var row PlatformAccountKey
	err := r.db.WithContext(ctx).
		Table("account").
		Select("platform, platform_account_id, handle").
		Where("account_id = ?", accountID).
		Limit(1).
		Scan(&row).Error
	if err != nil {
		return nil, classify(err)
	}
	if row.PlatformAccountID == "" {
		return nil, ErrNotFound
	}
	return &row, nil
}

// NetworkIDsForAccount returns every network an account is a member of, used to
// relabel history when it is allowlisted (US56).
func (r *NetworkRepository) NetworkIDsForAccount(ctx context.Context, platform, platformAccountID string) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	err := r.db.WithContext(ctx).
		Table("network_account AS na").
		Joins("JOIN account AS a ON a.account_id = na.account_id").
		Select("DISTINCT na.network_id").
		Where("a.platform = ? AND a.platform_account_id = ?", platform, platformAccountID).
		Scan(&ids).Error
	return ids, classify(err)
}

// ReportedNetworkIDs narrows a set of network ids to those a report has already
// been generated from.
//
// The query behind PRD-v1.4.md open question 9: allowlisting an account
// "suppresses and relabels" its historical networks, but a PDF citing accounts
// since allowlisted may already be in someone's inbox. The suppression cannot
// reach into that inbox; naming the exports at least makes the exposure
// answerable.
func (r *NetworkRepository) ReportedNetworkIDs(ctx context.Context, networkIDs []uuid.UUID) ([]uuid.UUID, error) {
	if len(networkIDs) == 0 {
		return nil, nil
	}
	var ids []uuid.UUID
	err := r.db.WithContext(ctx).
		Table("cis_network_reports").
		Select("DISTINCT network_id").
		Where("network_id IN ?", networkIDs).
		Scan(&ids).Error
	return ids, err
}

// UpsertReview writes the current review status overlay and appends to the
// immutable log, in one transaction (US52).
//
// The two writes are inseparable: an overlay updated without a log entry loses
// the decision history PRD 10.9.3 depends on, and a log entry without the
// overlay leaves the network displaying a status nobody chose.
func (r *NetworkRepository) UpsertReview(
	ctx context.Context,
	networkID uuid.UUID,
	fromStatus, toStatus, reason string,
	signalProfile models.JSONB,
	userID *uuid.UUID,
) (*models.CISNetworkReview, error) {
	now := time.Now().UTC()
	review := models.CISNetworkReview{
		NetworkID:  networkID,
		Status:     toStatus,
		Reason:     reason,
		ReviewedBy: userID,
		ReviewedAt: now,
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("network_id = ?", networkID).
			Assign(map[string]any{
				"status":      toStatus,
				"reason":      reason,
				"reviewed_by": userID,
				"reviewed_at": now,
				"updated_at":  now,
			}).
			FirstOrCreate(&review).Error; err != nil {
			return err
		}

		entry := models.CISNetworkReviewLog{
			NetworkID:     networkID,
			FromStatus:    fromStatus,
			ToStatus:      toStatus,
			Reason:        reason,
			SignalProfile: signalProfile,
			UserID:        userID,
			CreatedAt:     now,
		}
		return tx.Create(&entry).Error
	})
	if err != nil {
		return nil, err
	}
	return &review, nil
}

// ListReviewLog returns a network's status history, newest first (US59's
// internal briefing, and the audit trail generally).
func (r *NetworkRepository) ListReviewLog(ctx context.Context, networkID uuid.UUID, limit int) ([]models.CISNetworkReviewLog, error) {
	var rows []models.CISNetworkReviewLog
	q := r.db.WithContext(ctx).
		Where("network_id = ?", networkID).
		Order("created_at DESC, id DESC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	err := q.Find(&rows).Error
	return rows, err
}

// DismissalRow is one recorded false-positive dismissal with its snapshotted
// signal profile.
type DismissalRow struct {
	models.CISNetworkReviewLog `gorm:"embedded"`
	NetworkLabel               *string `gorm:"column:network_label"`
}

// ListDismissals returns the false-positive dismissals PRD 10.9.3 requires to
// be reviewable in aggregate.
//
// The signal profile comes from the LOG ROW, not from a join to
// coordinated_network. That is the whole point of snapshotting it at write
// time: a later detection run can recompute a network's scores, or recurrence
// can move it, and an aggregate built on drifting profiles cannot answer
// "which signal is systematically over-triggering?" — the question that decides
// whether beta_k or the thresholds need recalibrating.
func (r *NetworkRepository) ListDismissals(ctx context.Context, from, to *time.Time, limit, offset int) ([]DismissalRow, int64, error) {
	q := r.db.WithContext(ctx).
		Table("cis_network_review_log AS l").
		Where("l.to_status = ?", models.NetworkStatusDismissedFP)

	if from != nil {
		q = q.Where("l.created_at >= ?", *from)
	}
	if to != nil {
		q = q.Where("l.created_at <= ?", *to)
	}

	var total int64
	if err := q.Session(&gorm.Session{}).Select("COUNT(l.id)").Scan(&total).Error; err != nil {
		return nil, 0, err
	}

	var rows []DismissalRow
	err := q.Session(&gorm.Session{}).
		Joins("LEFT JOIN coordinated_network n ON n.network_id = l.network_id").
		Select("l.*, n.label AS network_label").
		Order("l.created_at DESC, l.id DESC").
		Limit(limit).
		Offset(offset).
		Scan(&rows).Error
	if err != nil {
		// The log is backend-owned and survives the pipeline's absence, but the
		// label join does not. Falling back keeps the recalibration view usable
		// on a database where the detector tables were dropped.
		if errors.Is(classify(err), ErrPipelineUnavailable) {
			var plain []models.CISNetworkReviewLog
			if e2 := r.db.WithContext(ctx).Where("to_status = ?", models.NetworkStatusDismissedFP).
				Order("created_at DESC").Limit(limit).Offset(offset).Find(&plain).Error; e2 != nil {
				return nil, 0, e2
			}
			out := make([]DismissalRow, 0, len(plain))
			for _, p := range plain {
				out = append(out, DismissalRow{CISNetworkReviewLog: p})
			}
			return out, total, nil
		}
		return nil, 0, err
	}
	return rows, total, nil
}

// DecisionCounts totals every terminal review decision in a window, for the
// precision figure PRD 10.9.3 sets a target on (> 0.85, rolling 90 days).
type DecisionCounts struct {
	Confirmed   int64 `gorm:"column:confirmed"`
	ActionTaken int64 `gorm:"column:action_taken"`
	Dismissed   int64 `gorm:"column:dismissed"`
}

// CountDecisions returns confirmed / action-taken / dismissed totals since a
// cutoff, counting each network's LATEST decision only.
//
// Counting log rows directly would let one network that was dismissed, reopened
// and confirmed contribute to both sides of the ratio.
func (r *NetworkRepository) CountDecisions(ctx context.Context, since time.Time) (*DecisionCounts, error) {
	const query = `
WITH latest AS (
	SELECT DISTINCT ON (network_id) network_id, to_status
	FROM cis_network_review_log
	WHERE created_at >= ?
	ORDER BY network_id, created_at DESC, id DESC
)
SELECT COUNT(*) FILTER (WHERE to_status = ?) AS confirmed,
       COUNT(*) FILTER (WHERE to_status = ?) AS action_taken,
       COUNT(*) FILTER (WHERE to_status = ?) AS dismissed
FROM latest`

	var out DecisionCounts
	err := r.db.WithContext(ctx).Raw(query, since,
		models.NetworkStatusConfirmed, models.NetworkStatusActionTaken, models.NetworkStatusDismissedFP,
	).Scan(&out).Error
	return &out, err
}

// ExpiredSnapshotNetworkIDs returns networks whose evidence snapshot has passed
// its retention date AND from which no report was ever generated.
//
// PRD 10.9.1 rule 7: snapshots are retained for a configurable period (default
// 24 months) and then purged, "except where an associated report has been
// generated, in which case the snapshot is retained as long as the report".
// That exception has teeth — a report whose evidence has been purged is
// worthless as evidence — so this can never be a blanket TTL delete.
//
// The backend only identifies them. The rows are AI-owned, so the actual purge
// is the pipeline's to perform; this list is what it is handed.
func (r *NetworkRepository) ExpiredSnapshotNetworkIDs(ctx context.Context, now time.Time, limit int) ([]uuid.UUID, error) {
	const query = `
SELECT s.network_id
FROM evidence_snapshot s
WHERE s.expires_at IS NOT NULL
  AND s.expires_at <= ?
  AND NOT EXISTS (SELECT 1 FROM cis_network_reports r WHERE r.network_id = s.network_id)
ORDER BY s.expires_at ASC
LIMIT ?`

	var ids []uuid.UUID
	err := r.db.WithContext(ctx).Raw(query, now, limit).Scan(&ids).Error
	return ids, classify(err)
}

// ActiveClaimIDsForDetection returns the claims a scheduled run should cover
// (PRD 10.5.8 item 1).
//
// Two filters, and both matter:
//
//   - status Active, resolved through the cis_claim_reviews overlay exactly as
//     F1 resolves it.
//   - claim_type Existing. PRD 10.3 puts detection over Non-Existing/Synthetic
//     claims out of scope: predicted claims have no real posts, so there is
//     nothing to cluster. Filtering on status alone would hand the pipeline a
//     batch of claims it cannot process.
func (r *NetworkRepository) ActiveClaimIDsForDetection(ctx context.Context, limit int) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	q := r.db.WithContext(ctx).
		Table("claims AS c").
		Joins("LEFT JOIN cis_claim_reviews AS rev ON rev.claim_id = c.id").
		Select("c.id").
		Where("COALESCE(rev.status, ?) = ?", models.ReviewStatusUnreviewed, models.ReviewStatusActive).
		Where("c.claim_type IN ?", models.ExistingClaimTypeValues).
		Order("c.final_claim_score DESC NULLS LAST, c.id ASC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	err := q.Scan(&ids).Error
	return ids, err
}

// VelocityTriggeredClaimIDs returns Active Existing claims whose Velocity has
// crossed the configured threshold (PRD 10.5.8 item 2).
//
// A sudden growth spike is exactly when a network is most likely present and
// most detectable, which is why this trigger exists at all.
func (r *NetworkRepository) VelocityTriggeredClaimIDs(ctx context.Context, threshold float64, limit int) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	q := r.db.WithContext(ctx).
		Table("claims AS c").
		Joins("LEFT JOIN cis_claim_reviews AS rev ON rev.claim_id = c.id").
		Select("c.id").
		Where("COALESCE(rev.status, ?) = ?", models.ReviewStatusUnreviewed, models.ReviewStatusActive).
		Where("c.claim_type IN ?", models.ExistingClaimTypeValues).
		Where("c.velocity_score IS NOT NULL AND c.velocity_score >= ?", threshold).
		Order("c.velocity_score DESC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	err := q.Scan(&ids).Error
	return ids, err
}

// String renders a failed-test list for logging.
func (f OfftopicFilter) String() string {
	return fmt.Sprintf("run=%v claim=%v test=%q", f.RunID, f.ClaimID, f.FailedTest)
}
