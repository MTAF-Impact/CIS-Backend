package dto

import (
	"time"

	"github.com/cis/cis-backend/internal/scoring"
)

// TopicRef is the compact topic label shown on every claim card.
type TopicRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ClaimCard is the list representation of a claim.
//
// There are two card variants. Rather than two incompatible shapes, this one
// struct carries the Existing-only fields as nullable and omits them entirely
// for Non-Existing claims, so the frontend can key off `claim_type` and reuse
// a single parser. Synthetic claims never carry a score, dates, or statement
// counts.
type ClaimCard struct {
	ID             string    `json:"id"`
	ClaimType      string    `json:"claim_type"` // existing | non_existing
	ClaimStatement string    `json:"claim_statement"`
	Topic          *TopicRef `json:"topic,omitempty"`
	ReviewStatus   string    `json:"review_status"` // unreviewed | active | inactive | action_taken
	CreatedAt      time.Time `json:"created_at"`

	// Existing/Generic claims only.
	FinalClaimScore        *float64   `json:"final_claim_score,omitempty"`
	FirstCaughtAt          *time.Time `json:"first_caught_at,omitempty"`
	PositiveStatementCount *int64     `json:"positive_statement_count,omitempty"`
	NegativeStatementCount *int64     `json:"negative_statement_count,omitempty"`
	IsDormant              *bool      `json:"is_dormant,omitempty"`
	// IsOnAlert drives the bell icon's filled/outline state.
	IsOnAlert *bool `json:"is_on_alert,omitempty"`

	// CoordinatedNetwork drives the coordinated-network indicator icon, so an
	// analyst sees during triage that a network is amplifying this claim
	// without opening it.
	//
	// OMITTED, not null, when nothing qualifies: there is no empty state for
	// this indicator. The field carries the full badge rather than a bare
	// boolean because this same card component is reused unmodified on the
	// policy detail page, and a second, policy-specific shape there would
	// defeat that reuse.
	CoordinatedNetwork *ClaimNetworkBadge `json:"coordinated_network,omitempty"`
}

// ClaimNetworkBadge is the "Coordinated network detected" indicator.
//
// # The gate behind this field
//
// Four conditions must ALL hold before this is populated:
//
//  1. a network_claim_link row exists for the claim with passed_relevance_gate
//     = true — anchoring a run to a claim does not make what it finds about
//     that claim;
//  2. the network's confidence band is Medium or High — there is no
//     low-confidence toggle on the claim list, so Low has no surface here;
//  3. the network's review status is NOT "Dismissed — False Positive" —
//     without this the claim page badges a network the team already examined
//     and concluded was organic;
//  4. the network is not suppressed — suppression binds to every surface: a
//     network invisible on the network detail page must not be reachable
//     from a claim card either.
//
// This is deliberately not expressed as a disjunction across band and review
// status: they are orthogonal axes, computed and assigned independently. It is
// an AND of all four, and the two axes appear as two separate conditions rather
// than one combined test.
type ClaimNetworkBadge struct {
	NetworkID         string  `json:"network_id"`
	Label             string  `json:"label"`
	CoordinationScore float64 `json:"coordination_score"`
	ConfidenceBand    string  `json:"confidence_band"`
	// ReviewStatus is displayed, not merely used for filtering: an analyst
	// deciding whether to rebut or refer must not read "Unreviewed, Medium"
	// and "Confirmed, High" identically.
	ReviewStatus string `json:"review_status"`
	AccountCount int    `json:"account_count"`
	// OtherCount is how many further networks also qualify for this claim.
	// Only the highest-scoring one is shown in full, plus a count of the rest.
	OtherCount int `json:"other_count"`
	// DetailURL links through to the network detail page.
	DetailURL string `json:"detail_url"`
}

// HarmBreakdown exposes the Harm Severity sub-scores.
//
// These four are the only manually editable values in the whole score;
// R, V, F and EI remain AI-only. Editable is therefore a property of this
// struct, not of ScoreBreakdown.
type HarmBreakdown struct {
	PublicSafety       *float64            `json:"public_safety"`
	InstitutionalTrust *float64            `json:"institutional_trust"`
	Economic           *float64            `json:"economic"`
	PolicyDisruption   *float64            `json:"policy_disruption"`
	HumanConfirmed     bool                `json:"human_confirmed"`
	Weights            scoring.HarmWeights `json:"weights"`

	// Edit is the audit trail of the last human override: who changed
	// the sub-scores and when. Omitted while the values are the AI's originals,
	// which is what lets the UI mark an edited H distinctly from an AI-original
	// one wherever the badge appears.
	Edit *HarmEditAudit `json:"edit,omitempty"`
}

// HarmEditAudit records a human override of the Harm sub-scores.
type HarmEditAudit struct {
	EditedBy *string   `json:"edited_by"`
	EditedAt time.Time `json:"edited_at"`
	// Previous holds the AI's classification before the override, so the
	// original is recoverable from the page as well as the audit table.
	Previous HarmPrevious `json:"previous"`
}

// HarmPrevious is the pre-override Harm classification.
type HarmPrevious struct {
	PublicSafety       *float64 `json:"public_safety"`
	InstitutionalTrust *float64 `json:"institutional_trust"`
	Economic           *float64 `json:"economic"`
	PolicyDisruption   *float64 `json:"policy_disruption"`
	HarmScore          *float64 `json:"harm_score"`
}

// ScoreBreakdown is the Score Transparency payload.
//
// Every component is returned together with the collapsed FinalClaimScore, so
// the final number is never shown without access to its inputs.
type ScoreBreakdown struct {
	Reach              *float64 `json:"reach"`               // R, 0-100
	Velocity           *float64 `json:"velocity"`            // V, 0-100
	Falseness          *float64 `json:"falseness"`           // F, 0-100
	Harm               *float64 `json:"harm"`                // H, 0-100
	EmotionalIntensity *float64 `json:"emotional_intensity"` // EI supporting side, 0-100
	// EmotionalIntensityOpposing is display-only and never enters the score.
	EmotionalIntensityOpposing *float64 `json:"emotional_intensity_opposing"`

	HarmBreakdown HarmBreakdown `json:"harm_breakdown"`

	ClaimScore      *float64 `json:"claim_score"`       // composite, pre-discount, 0-100
	NPR             *float64 `json:"npr"`               // 0-1, nil when dormant
	DiscountFactor  *float64 `json:"discount_factor"`   // 0.5-1, nil when dormant
	FinalClaimScore *float64 `json:"final_claim_score"` // 0-100, the ranking value

	IsDormant bool            `json:"is_dormant"`
	Note      string          `json:"note,omitempty"`
	Weights   scoring.Weights `json:"weights"`

	// Formula is the plain-language explanation behind the info-tooltip.
	// Served rather than hard-coded in the frontend so the words and the
	// weights above can never drift apart.
	Formula string `json:"formula"`
}

// PolicyRef is a policy correlated with a claim, via either the many-to-many
// join used by Existing claims or the single policy_id column used by
// Synthetic claims.
type PolicyRef struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	Source        string     `json:"source"` // cis = registered through the Public Policy Bank, ai = created by the AI service
	AIPolicyID    *string    `json:"ai_policy_id,omitempty"`
	Status        *string    `json:"status,omitempty"` // rolled_out | not_rolled_out
	RolledOutDate *time.Time `json:"rolled_out_date,omitempty"`
	HasDocument   bool       `json:"has_document"`
}

// AccountRef is one row of the Top 5 Accounts panel.
//
// Ranked over the Supporting-side cluster, matching the Reach parameter's
// scope.
type AccountRef struct {
	Rank             int    `json:"rank"`
	AuthorID         string `json:"author_id"`
	ContentCount     int64  `json:"content_count"`
	TotalImpressions int64  `json:"total_impressions"`
}

// DebunkBlocks is the Truth Sandwich, split into the three labelled sections
// the AI service writes alongside the flat `activity_content` paragraph.
//
// Existing/Generic claims only. Absent when the AI service has written none of
// the three, which is the case for every Synthetic claim and for any Existing
// claim generated before the split existed.
type DebunkBlocks struct {
	CoreFact       *string `json:"core_fact"`
	NuancedFlag    *string `json:"nuanced_flag"`
	ReiteratedFact *string `json:"reiterated_fact"`
}

// ActivityContent is the cached AI-generated Debunk or Prebunk draft. It is
// generated once by the AI service and served from cache; viewing a claim
// never triggers a new generation.
type ActivityContent struct {
	Type        string     `json:"type"` // debunk | prebunk
	Content     *string    `json:"content"`
	GeneratedAt *time.Time `json:"generated_at"`
	Available   bool       `json:"available"`
	// Debunk carries the same content pre-split into three blocks, for a
	// frontend that renders them as distinct labelled sections rather than one
	// paragraph. `content` remains the copyable single block.
	Debunk *DebunkBlocks `json:"debunk,omitempty"`

	// Segments are the per-audience-segment recommendations: one tailored,
	// individually-copyable draft per segment affected by the claim, ordered
	// most-exposed first.
	//
	// Empty on a deployment whose AI service has not shipped segmentation yet,
	// and on Synthetic claims, where the Prebunk draft is not segmented. The
	// frontend falls back to `content` when it is empty; it must never merge
	// the segments into one box, since targeting is the whole point.
	Segments []DebunkSegment `json:"segments"`
}

// DebunkSegment is one audience-segment-specific Debunk recommendation.
type DebunkSegment struct {
	Segment string `json:"segment"`
	// Rationale is why this segment was identified — the framing or concern the
	// copy addresses. Shown as the card's subtitle.
	Rationale   *string   `json:"rationale,omitempty"`
	Content     string    `json:"content"`
	GeneratedAt time.Time `json:"generated_at"`
}

// ClaimReview is the reviewer's latest note on a claim's status, read back on
// the detail page. It reflects only the most recent decision recorded by
// PUT /claims/:id/status — cis_claim_reviews is a single overlay row per
// claim, not a change log, so earlier notes are not retained.
type ClaimReview struct {
	Notes      *string    `json:"notes"`
	ReviewedBy *string    `json:"reviewed_by"`
	ReviewedAt *time.Time `json:"reviewed_at"`
}

// ClaimDetail is the claim detail page payload.
type ClaimDetail struct {
	ID             string       `json:"id"`
	ClaimType      string       `json:"claim_type"`
	ClaimStatement string       `json:"claim_statement"`
	Topic          *TopicRef    `json:"topic,omitempty"`
	ReviewStatus   string       `json:"review_status"`
	Review         *ClaimReview `json:"review,omitempty"`
	CreatedAt      time.Time    `json:"created_at"`
	UpdatedAt      time.Time    `json:"updated_at"`

	Activity ActivityContent `json:"activity"`
	Policies []PolicyRef     `json:"policies"`

	// Existing/Generic claims only.
	FirstCaughtAt          *time.Time      `json:"first_caught_at,omitempty"`
	ScoreBreakdown         *ScoreBreakdown `json:"score_breakdown,omitempty"`
	TopAccounts            []AccountRef    `json:"top_accounts,omitempty"`
	PositiveStatementCount *int64          `json:"positive_statement_count,omitempty"`
	NegativeStatementCount *int64          `json:"negative_statement_count,omitempty"`
	IsOnAlert              *bool           `json:"is_on_alert,omitempty"`

	// CoordinatedNetwork is the "Coordinated network detected" indicator.
	// Omitted when nothing qualifies — no empty state.
	//
	// This is the point of network detection in daily use: it decides whether
	// the team publicly rebuts a claim or refers it to the platform instead.
	// Rebutting a claim that only 40 accounts are actually making hands it the
	// reach it was engineered to obtain.
	CoordinatedNetwork *ClaimNetworkBadge `json:"coordinated_network,omitempty"`
}

// Statement is one source post backing a claim.
type Statement struct {
	ID                    string    `json:"id"`
	Text                  string    `json:"text"`
	Source                string    `json:"source"`
	AuthorID              *string   `json:"author_id"`
	Location              *string   `json:"location"`
	Stance                *string   `json:"stance"`
	OutrageScore          *float64  `json:"outrage_score"`
	Impressions           *int      `json:"impressions"`
	PositiveReactionCount *int      `json:"positive_reaction_count"`
	NegativeReactionCount *int      `json:"negative_reaction_count"`
	CreatedAt             time.Time `json:"created_at"`
}

// ClaimSection is one of the Claim Repository Bank's two sections, S1
// (Existing claims) or S2 (Non-Existing claims).
//
// Each section paginates independently: the caller picks its own page/limit
// per section (S1 and S2 typically have very different pool sizes), so each
// section carries its own pagination window rather than sharing one at the
// RepositoryResponse level.
type ClaimSection struct {
	Section     string      `json:"section"` // S1 | S2
	Label       string      `json:"label"`
	ClaimType   string      `json:"claim_type"`
	SortedBy    string      `json:"sorted_by"`
	TotalInPool int64       `json:"total_in_pool"`
	Page        int         `json:"page"`
	Limit       int         `json:"limit"`
	TotalPages  int         `json:"total_pages"`
	Claims      []ClaimCard `json:"claims"`
}

// RepositoryResponse is the whole Claim Repository Bank page in one call.
//
// Both sections are always present regardless of the selected status tab: the
// status filter narrows claims *within* each section and never hides a
// section entirely.
type RepositoryResponse struct {
	LastFetchedAt time.Time    `json:"last_fetched_at"`
	AppliedStatus string       `json:"applied_status"`
	AppliedTopics []string     `json:"applied_topics"`
	Existing      ClaimSection `json:"existing"`
	NonExisting   ClaimSection `json:"non_existing"`
}

// UpdateClaimStatusRequest is the body of PUT /api/v1/claims/:id/status.
type UpdateClaimStatusRequest struct {
	Status string  `json:"status" validate:"required,oneof=unreviewed active inactive action_taken"`
	Notes  *string `json:"notes" validate:"omitempty,max=2000"`
}

// ClaimStatusResponse confirms a status change.
type ClaimStatusResponse struct {
	ClaimID      string    `json:"claim_id"`
	ReviewStatus string    `json:"review_status"`
	Notes        *string   `json:"notes"`
	ReviewedAt   time.Time `json:"reviewed_at"`
	ReviewedBy   *string   `json:"reviewed_by"`
}

// ConfirmHarmRequest is the body of PUT /api/v1/claims/:id/harm/confirm.
//
// Every field is optional: an omitted sub-score keeps the AI service's own
// classification, and an empty body is the legitimate "I reviewed these and
// they are right" case, which still flips harm_human_confirmed.
type ConfirmHarmRequest struct {
	PublicSafety       *float64 `json:"public_safety" validate:"omitempty,gte=0,lte=100"`
	InstitutionalTrust *float64 `json:"institutional_trust" validate:"omitempty,gte=0,lte=100"`
	Economic           *float64 `json:"economic" validate:"omitempty,gte=0,lte=100"`
	PolicyDisruption   *float64 `json:"policy_disruption" validate:"omitempty,gte=0,lte=100"`
}

// ScorePoint is one bucket of the Alert page's score history chart.
type ScorePoint struct {
	BucketStart     time.Time `json:"bucket_start"`
	FinalClaimScore *float64  `json:"final_claim_score"`
	ClaimScore      *float64  `json:"claim_score"`
	SampleCount     int64     `json:"sample_count"`
}

// ScoreHistoryResponse is the per-claim score time series.
type ScoreHistoryResponse struct {
	ClaimID     string       `json:"claim_id"`
	Granularity string       `json:"granularity"`
	Points      []ScorePoint `json:"points"`
}
