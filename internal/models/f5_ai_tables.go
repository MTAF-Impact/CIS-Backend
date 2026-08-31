package models

import (
	"time"

	"github.com/google/uuid"
)

// F5 — Coordinated-Network Detector: the pipeline's output tables.
//
// # Ownership
//
// Every table in this file is written by the AI service's detection pipeline
// and is READ ONLY here, exactly like the tables in ai_tables.go. The backend
// never inserts, updates, deletes, or migrates them. See PRD 10.10, and
// docs/local_docs/PRD-v1.4.md 6.1 for the ownership split and why
// `coordinated_network.review_status` and `coordination_allowlist` were moved
// out of this file and onto backend-owned tables instead.
//
// # Two deliberate omissions from PRD 10.10
//
//  1. `coordinated_network.review_status` is NOT mapped. PRD 10.10 declares it
//     as a column here, but a human's assessment living on a table the pipeline
//     rewrites would be erased by the next detection run. It lives in the
//     cis_network_reviews overlay instead, the same shape and for the same
//     reason as cis_claim_reviews. Mapping it here as well would give the
//     codebase two answers to "what is this network's status?".
//  2. `coordination_allowlist` is backend-owned (cis_coordination_allowlist).
//     It records human decisions, and the pipeline reads it rather than writes
//     it — the one place the read direction between the two services reverses.
//
// # Fields beyond PRD 10.10
//
// Section 8.3 of the gap analysis catalogues nine fields Section 10 requires
// somewhere but 10.10 declares no column for. The ones the backend cannot work
// around are added here and marked "BEYOND 10.10". They need AI-team sign-off;
// docs/sql/01_f5_reference_schema.sql carries the same DDL for that
// conversation.

// Confidence bands (PRD 10.6.2). Computed by the pipeline, never set by a human.
const (
	ConfidenceLow    = "low"
	ConfidenceMedium = "medium"
	ConfidenceHigh   = "high"
)

// SurfaceableConfidenceBands are the bands a network may be shown at without an
// explicit opt-in. Low is reachable in F5 only behind the "Show low-confidence"
// toggle (PRD 10.6.3 rule 2) and is never reachable from F1 at all (rule 5).
var SurfaceableConfidenceBands = []string{ConfidenceMedium, ConfidenceHigh}

// IsValidConfidenceBand reports whether s is one of the three bands.
func IsValidConfidenceBand(s string) bool {
	return s == ConfidenceLow || s == ConfidenceMedium || s == ConfidenceHigh
}

// Detection run lifecycle states.
const (
	RunStatusPending   = "pending"
	RunStatusRunning   = "running"
	RunStatusCompleted = "completed"
	RunStatusFailed    = "failed"
)

// Detection run trigger sources (PRD 10.5.8).
const (
	RunTriggerScheduled = "scheduled"
	RunTriggerVelocity  = "velocity"
	RunTriggerOnDemand  = "on_demand"
)

// Membership roles on network_account.
//
// BEYOND 10.10 (gap 7). US51 requires the graph view to render "genuine
// unclustered accounts active on the same claim, in a visually distinct style,
// for contrast", and PRD 10.8 item 5 requires the report to print the count of
// them. network_account is keyed on network_id and therefore holds members
// only, so the contrast set has nowhere to live. A role discriminator is the
// smaller of the two possible fixes, the other being a second table.
const (
	MembershipMember     = "member"
	MembershipComparison = "comparison"
)

// Which relevance-gate test an off-topic cluster failed (PRD 10.5.1a).
const (
	FailedTestAnchoring    = "anchoring"
	FailedTestEvidenceVol  = "evidence_volume"
	FailedTestLinkStrength = "link_strength"
)

// AIDetectionRun is the AI service's `detection_run` table: one execution of
// the pipeline over a defined scope and window. READ ONLY.
//
// ParametersJSON is the whole parameter set in force when the run executed.
// PRD US62 requires that changing a parameter never retroactively alters a
// stored detection, so a report generated months later reads its configuration
// from here rather than from the current settings row.
type AIDetectionRun struct {
	ID uuid.UUID `gorm:"column:run_id;type:uuid;primaryKey"`

	// ScopeClaimIDs are the claims the run was anchored to: one for a
	// claim-scoped run, many for a topic batch (PRD 10.5.1).
	ScopeClaimIDs StringList `gorm:"column:scope_claim_ids"`
	TriggerSource string     `gorm:"column:trigger_source"`

	WindowStart time.Time `gorm:"column:window_start"`
	WindowEnd   time.Time `gorm:"column:window_end"`

	ParametersJSON JSONB   `gorm:"column:parameters_json"`
	ModelVersions  JSONB   `gorm:"column:model_versions"`
	RandomSeed     *int64  `gorm:"column:random_seed"`
	LibraryVersion *string `gorm:"column:library_version"`

	// SignalsUnavailable names the families that could not be measured. Two or
	// more caps every network in the run at Medium confidence (PRD 10.6.3
	// rule 4), which is why it is a run-level fact the network list has to
	// surface — "why is everything Medium this week?" is a question about runs.
	SignalsUnavailable StringList `gorm:"column:signals_unavailable"`

	// Truncated records that |A| exceeded A_max and the candidate set was cut.
	// PRD 10.5.1 requires this to be displayed on the network detail page, not
	// merely stored: a truncated run has known incomplete recall and the
	// analyst has to be told at the point of judgement.
	Truncated       bool `gorm:"column:truncated_bool"`
	CandidatesCount int  `gorm:"column:candidates_count"`

	Status      string     `gorm:"column:status"`
	Error       *string    `gorm:"column:error"`
	StartedAt   time.Time  `gorm:"column:started_at"`
	CompletedAt *time.Time `gorm:"column:completed_at"`
}

// TableName pins the AI-owned table name.
func (AIDetectionRun) TableName() string { return "detection_run" }

// CapsConfidenceAtMedium reports whether PRD 10.6.3 rule 4 applies to every
// network produced by this run: a truncated candidate set, or two or more
// signal families unavailable, caps the whole run at Medium regardless of score.
func (r AIDetectionRun) CapsConfidenceAtMedium() bool {
	return r.Truncated || len(r.SignalsUnavailable) >= 2
}

// AICoordinatedNetwork is the AI service's `coordinated_network` table: one
// detected cluster, the user-facing object of F5. READ ONLY.
//
// review_status is deliberately absent — see the file comment.
type AICoordinatedNetwork struct {
	ID    uuid.UUID `gorm:"column:network_id;type:uuid;primaryKey"`
	RunID uuid.UUID `gorm:"column:run_id;type:uuid"`
	Label string    `gorm:"column:label"`

	// Cluster metrics (PRD 10.5.5), each 0-100.
	CoordinationScore float64 `gorm:"column:coordination_score"`
	SY                float64 `gorm:"column:sy"` // Synchrony
	DU                float64 `gorm:"column:du"` // Duplication
	CO                float64 `gorm:"column:co"` // Cohesion
	PR                float64 `gorm:"column:pr"` // Provenance anomaly
	AU                float64 `gorm:"column:au"` // Automation & behavioural anomaly

	// SignalBreadth is the count of distinct signal families independently
	// scoring >= 50 (PRD 10.4). It is computed by the pipeline and stored, not
	// derived here: PRD 10.6.1 prints it in every report, and a value the
	// backend recomputed under a different reading of "signal family" would not
	// match the PDF it was printed in. See PRD-v1.4.md 4.5 and open question 7.
	SignalBreadth  int    `gorm:"column:signal_breadth"`
	ConfidenceBand string `gorm:"column:confidence_band"`

	// RawCounts holds the underlying integer observation behind each metric.
	// US50 requires the raw counts, not just the normalised scores ("43 of 47
	// accounts posted within the same 6-minute window, 3 times in 24h").
	RawCounts JSONB `gorm:"column:raw_counts_json"`

	AccountCount int        `gorm:"column:account_count"`
	PostCount    int        `gorm:"column:post_count"`
	Platforms    StringList `gorm:"column:platforms"`
	InternalDens float64    `gorm:"column:internal_density"`
	Conductance  float64    `gorm:"column:conductance"`

	// ComparisonAccountCount is the number of genuine unclustered accounts
	// active on the same claim, rendered for contrast in the graph (US51) and
	// printed in the report (PRD 10.8 item 5).
	//
	// BEYOND 10.10 (gap 7).
	ComparisonAccountCount int `gorm:"column:comparison_account_count"`

	// FingerprintHash and ParentNetworkID implement recurrence (PRD 10.5.7).
	// A cluster whose member-set Jaccard against a stored fingerprint is >= 0.50
	// is the same network resurfacing, and inherits its history through the
	// parent chain.
	FingerprintHash string     `gorm:"column:fingerprint_hash"`
	ParentNetworkID *uuid.UUID `gorm:"column:parent_network_id;type:uuid"`

	// AllowlistSuppressed marks a network whose membership is >= 60%
	// allowlisted (PRD 10.6.3 rule 3). Such a network is suppressed entirely,
	// on every surface, and logged as an allowlist hit. It is stored rather
	// than recomputed so the suppression is stable for a given detection even
	// as the allowlist changes underneath it.
	AllowlistSuppressed bool `gorm:"column:allowlist_suppressed"`

	// Relabelled records that US56 retroactively marked this network after one
	// of its members was allowlisted.
	Relabelled bool `gorm:"column:relabelled"`

	DetectedAt time.Time `gorm:"column:created_at"`
}

// TableName pins the AI-owned table name.
func (AICoordinatedNetwork) TableName() string { return "coordinated_network" }

// AINetworkAccount is the AI service's `network_account` table: one account's
// membership of one network, with its individual behavioural metrics. READ ONLY.
//
// PRD 10.10: the account is the durable entity, the membership is per-detection.
type AINetworkAccount struct {
	NetworkID uuid.UUID `gorm:"column:network_id;type:uuid;primaryKey"`
	AccountID uuid.UUID `gorm:"column:account_id;type:uuid;primaryKey"`

	// MembershipRole separates cluster members from the contrast set.
	// BEYOND 10.10 (gap 7).
	MembershipRole string `gorm:"column:membership_role"`

	PostsInCluster        int      `gorm:"column:posts_in_cluster"`
	DuplicationRate       float64  `gorm:"column:duplication_rate"`
	MedianInterpostSecs   *float64 `gorm:"column:median_interpost_interval_seconds"`
	CircadianCoverage     float64  `gorm:"column:circadian_coverage"`
	DegreeCentrality      float64  `gorm:"column:degree_centrality"`
	EigenvectorCentrality float64  `gorm:"column:eigenvector_centrality"`

	// ScoreContribution is this account's individual contribution to each
	// cluster metric (PRD 10.5.6 item 4).
	ScoreContribution JSONB `gorm:"column:score_contribution_json"`

	// LayoutX/LayoutY are the precomputed ForceAtlas2 coordinates.
	//
	// BEYOND 10.10 (gap 4). PRD 10.5.6 item 5 requires them "so the UI and the
	// PDF render identically", and PRD 10.8 requires byte-identical report
	// regeneration. A force-directed layout lands somewhere different on every
	// run, so this is the one piece of the snapshot that genuinely cannot be
	// recomputed at render time.
	LayoutX *float64 `gorm:"column:layout_x"`
	LayoutY *float64 `gorm:"column:layout_y"`
}

// TableName pins the AI-owned table name.
func (AINetworkAccount) TableName() string { return "network_account" }

// AIAccount is the AI service's `account` table: the durable social-media
// account entity. READ ONLY.
//
// PRD 10.4 constrains what may be stored: "public handle and platform-issued ID
// only". There is deliberately no real-name column, no inferred identity, and
// no bot verdict anywhere in this struct, per governance rules 2 and 3
// (PRD 10.9.1).
type AIAccount struct {
	ID              uuid.UUID `gorm:"column:account_id;type:uuid;primaryKey"`
	Platform        string    `gorm:"column:platform"`
	PlatformAccount string    `gorm:"column:platform_account_id"`
	Handle          string    `gorm:"column:handle"`

	// CreatedAtPlatform is the account's own creation date, feeding the
	// creation-time proximity sub-signal of w_meta (PRD 10.5.2.4).
	CreatedAtPlatform *time.Time `gorm:"column:created_at_platform"`
	// ProfileHash is a perceptual hash (pHash) of the profile image.
	ProfileHash *string `gorm:"column:profile_hash"`

	// Bio, DeclaredLocation and ClientApp back three of w_meta's five
	// sub-signals (PRD 10.5.2.4).
	//
	// BEYOND 10.10 (gap 1). PRD 10.10's `account` declares only
	// created_at_platform, handle and profile_hash, which leaves bio similarity
	// and declared-location/client-string identity with no source column at all.
	Bio              *string `gorm:"column:bio"`
	DeclaredLocation *string `gorm:"column:declared_location"`
	ClientApp        *string `gorm:"column:client_app"`

	FirstSeen time.Time `gorm:"column:first_seen"`
	LastSeen  time.Time `gorm:"column:last_seen"`
}

// TableName pins the AI-owned table name.
func (AIAccount) TableName() string { return "account" }

// AINetworkEdge is the AI service's `network_edge` table: one retained
// behavioural edge with its per-family decomposition. READ ONLY.
//
// This table is what makes membership explainable. PRD 10.5.3: "Every retained
// edge stores its per-family decomposition, so any account's membership can be
// explained down to the specific behaviours that connected it." US55 turns that
// into a hard product rule — no account may appear in a network without a
// viewable reason.
type AINetworkEdge struct {
	NetworkID uuid.UUID `gorm:"column:network_id;type:uuid;primaryKey"`
	AccountA  uuid.UUID `gorm:"column:account_a;type:uuid;primaryKey"`
	AccountB  uuid.UUID `gorm:"column:account_b;type:uuid;primaryKey"`

	WTotal  float64 `gorm:"column:w_total"`
	WTime   float64 `gorm:"column:w_time"`   // temporal synchrony
	WText   float64 `gorm:"column:w_text"`   // content duplication
	WAmp    float64 `gorm:"column:w_amp"`    // co-amplification
	WMeta   float64 `gorm:"column:w_meta"`   // provenance
	WStruct float64 `gorm:"column:w_struct"` // structural overlap

	// SignalCount is how many families cleared 0.25 on this edge. The
	// multi-signal rule (PRD 10.5.3) requires at least two, and it is the
	// pipeline's primary false-positive control: "Synchrony alone is a
	// timezone. Duplication alone is a hashtag. Provenance alone is a signup
	// surge. Only their conjunction is evidence."
	SignalCount int `gorm:"column:signal_count"`
}

// TableName pins the AI-owned table name.
func (AINetworkEdge) TableName() string { return "network_edge" }

// AINetworkEvidencePost is the AI service's `network_evidence_post` table: the
// immutable, hashed capture of one post as it existed at detection time.
// READ ONLY.
//
// PRD 10.5.6: operators delete their own content once a campaign concludes, so
// a report built from live data two weeks later documents an empty set. US54
// renders from this table and never re-fetches, which is why a deleted post can
// still be shown, marked "no longer publicly available".
type AINetworkEvidencePost struct {
	ID        uuid.UUID `gorm:"column:evidence_id;type:uuid;primaryKey"`
	NetworkID uuid.UUID `gorm:"column:network_id;type:uuid"`
	AccountID uuid.UUID `gorm:"column:account_id;type:uuid"`

	PostPlatformID string `gorm:"column:post_platform_id"`
	CapturedText   string `gorm:"column:captured_text"`

	// PostedAt and CapturedAt are two different times, and PRD 10.10 declares
	// them as two columns for that reason: PostedAt is when the account
	// published, CapturedAt is when the pipeline snapshotted it. Every temporal
	// signal depends on the first. The current content_items table has only a
	// capture time — see PRD-v1.4.md Section 5, the blocker.
	PostedAt   time.Time `gorm:"column:posted_at"`
	CapturedAt time.Time `gorm:"column:captured_at"`

	ContentSHA256 string `gorm:"column:content_sha256"`

	DuplicateGroupID *uuid.UUID `gorm:"column:duplicate_group_id;type:uuid"`
	IsCanonical      bool       `gorm:"column:is_canonical"`
	StillPublic      bool       `gorm:"column:still_public_bool"`

	// SharedSpanStart/End locate the span this variant shares with its group's
	// canonical text. US54 requires the shared span highlighted, and PRD 10.8
	// item 6 requires the same highlighting in the PDF — so the offsets are
	// computed once and stored rather than derived in the browser, which would
	// leave the report unable to reproduce them.
	//
	// BEYOND 10.10.
	SharedSpanStart *int `gorm:"column:shared_span_start"`
	SharedSpanEnd   *int `gorm:"column:shared_span_end"`
}

// TableName pins the AI-owned table name.
func (AINetworkEvidencePost) TableName() string { return "network_evidence_post" }

// AINetworkBurstBin is the AI service's `network_burst_bin` table: one bin of
// the burst timeline (US53, PRD 10.5.6 item 2). READ ONLY.
type AINetworkBurstBin struct {
	NetworkID       uuid.UUID `gorm:"column:network_id;type:uuid;primaryKey"`
	BinStart        time.Time `gorm:"column:bin_start;primaryKey"`
	BinWidthSeconds int       `gorm:"column:bin_width_seconds"`
	PostCount       int       `gorm:"column:post_count"`
	ZScore          float64   `gorm:"column:zscore"`
	IsAnomalous     bool      `gorm:"column:is_anomalous"`
}

// TableName pins the AI-owned table name.
func (AINetworkBurstBin) TableName() string { return "network_burst_bin" }

// AINetworkClaimLink is the AI service's `network_claim_link` table: the
// many-to-many network<->claim relation carrying the claim-relevance gate's
// verdict. READ ONLY.
//
// PRD 10.10: exactly one row per network carries IsPrimaryClaim = true — the
// claim with the highest overlap_ratio. The rest are secondary links above
// omega_min. PassedRelevanceGate is the first of US61's four conditions for
// showing a cross-link on an F1 claim page.
type AINetworkClaimLink struct {
	NetworkID uuid.UUID `gorm:"column:network_id;type:uuid;primaryKey"`
	ClaimID   uuid.UUID `gorm:"column:claim_id;type:uuid;primaryKey"`

	// OverlapRatio is C's posts in this claim's supporting cluster over C's
	// posts across all monitored content in W (PRD 10.5.1a). It answers a
	// question no signal score can: not "is this coordinated?" but "is this
	// coordinated *about our claim*?".
	OverlapRatio float64 `gorm:"column:overlap_ratio"`
	// AnchoringShare is the share of members with >= 2 posts in the claim
	// cluster.
	AnchoringShare      float64 `gorm:"column:anchoring_share"`
	ClaimClusterPostCnt int     `gorm:"column:claim_cluster_post_count"`

	IsPrimaryClaim      bool `gorm:"column:is_primary_claim"`
	PassedRelevanceGate bool `gorm:"column:passed_relevance_gate"`
}

// TableName pins the AI-owned table name.
func (AINetworkClaimLink) TableName() string { return "network_claim_link" }

// AIOfftopicCluster is the AI service's `offtopic_cluster` table: a genuinely
// coordinated cluster that failed the claim-relevance gate. READ ONLY.
//
// PRD 10.5.1a: these are real coordinated clusters — spam rings, engagement
// farms, unrelated political amplification — that happened to pass through the
// claim. They are not the city's problem and must never appear in a climate
// report. They are retained only so an admin can see whether omega_min is set
// too loose or too tight (US62), which is the single read-only surface they are
// ever exposed on.
type AIOfftopicCluster struct {
	ID      uuid.UUID `gorm:"column:cluster_id;type:uuid;primaryKey"`
	RunID   uuid.UUID `gorm:"column:run_id;type:uuid"`
	ClaimID uuid.UUID `gorm:"column:claim_id;type:uuid"`

	CoordinationSignals JSONB   `gorm:"column:coordination_signals_json"`
	OverlapRatio        float64 `gorm:"column:overlap_ratio"`
	AnchoringShare      float64 `gorm:"column:anchoring_share"`
	AccountCount        int     `gorm:"column:account_count"`
	PostCount           int     `gorm:"column:post_count"`
	FingerprintHash     string  `gorm:"column:fingerprint_hash"`

	// FailedTest records which of the three gate tests rejected it, which is
	// the whole diagnostic value of the table.
	FailedTest string    `gorm:"column:failed_test"`
	CreatedAt  time.Time `gorm:"column:created_at"`
}

// TableName pins the AI-owned table name.
func (AIOfftopicCluster) TableName() string { return "offtopic_cluster" }

// AIEvidenceSnapshot is the AI service's `evidence_snapshot` table: a
// first-class identity and digest for the evidence captured for one network.
// READ ONLY.
//
// BEYOND 10.10 (gap 8). PRD 10.8 item 10 requires the report's chain-of-custody
// section to print "evidence snapshot ID, snapshot hash, detection run ID, and
// the export audit entry ID". Section 10.10 supplies the run id and the audit
// id, and gives a per-*post* content_sha256 — but declares no snapshot entity,
// no snapshot id, and no digest over the snapshot as a whole. Without this row
// the chain of custody has three of its four fields, and US60's integrity claim
// ("the manifest hashes establish that the bundle was not modified after
// generation") covers the files but not the evidence they were built from.
type AIEvidenceSnapshot struct {
	ID        uuid.UUID `gorm:"column:snapshot_id;type:uuid;primaryKey"`
	NetworkID uuid.UUID `gorm:"column:network_id;type:uuid"`
	RunID     uuid.UUID `gorm:"column:run_id;type:uuid"`

	// SnapshotSHA256 is computed over a canonical serialisation of every
	// evidence row belonging to this network.
	SnapshotSHA256 string `gorm:"column:snapshot_sha256"`

	EvidencePostCount int       `gorm:"column:evidence_post_count"`
	CreatedAt         time.Time `gorm:"column:created_at"`
	// ExpiresAt implements PRD 10.9.1 rule 7's default 24-month retention. The
	// backend's purge job clears the date once a report has been generated from
	// the snapshot, because then it must live as long as the report.
	ExpiresAt *time.Time `gorm:"column:expires_at"`
}

// TableName pins the AI-owned table name.
func (AIEvidenceSnapshot) TableName() string { return "evidence_snapshot" }
