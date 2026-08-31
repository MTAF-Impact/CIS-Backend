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
// The PRD defines two card variants (5.5). Rather than two incompatible
// shapes, this one struct carries the Existing-only fields as nullable and
// omits them entirely for Non-Existing claims, so the frontend can key off
// `claim_type` and reuse a single parser. Per US18, Synthetic claims never
// carry a score, dates, or statement counts.
type ClaimCard struct {
	ID             string    `json:"id"`
	ClaimType      string    `json:"claim_type"` // existing | non_existing
	ClaimStatement string    `json:"claim_statement"`
	Topic          *TopicRef `json:"topic,omitempty"`
	ReviewStatus   string    `json:"review_status"` // unreviewed | active | inactive | action_taken
	CreatedAt      time.Time `json:"created_at"`

	// Existing/Generic claims only (US10).
	FinalClaimScore        *float64   `json:"final_claim_score,omitempty"`
	FirstCaughtAt          *time.Time `json:"first_caught_at,omitempty"`
	PositiveStatementCount *int64     `json:"positive_statement_count,omitempty"`
	NegativeStatementCount *int64     `json:"negative_statement_count,omitempty"`
	IsDormant              *bool      `json:"is_dormant,omitempty"`
	// IsOnAlert drives the bell icon's filled/outline state (US14).
	IsOnAlert *bool `json:"is_on_alert,omitempty"`
}

// HarmBreakdown exposes the Harm Severity sub-scores (PRD 6.2.4).
type HarmBreakdown struct {
	PublicSafety       *float64            `json:"public_safety"`
	InstitutionalTrust *float64            `json:"institutional_trust"`
	Economic           *float64            `json:"economic"`
	PolicyDisruption   *float64            `json:"policy_disruption"`
	HumanConfirmed     bool                `json:"human_confirmed"`
	Weights            scoring.HarmWeights `json:"weights"`
}

// ScoreBreakdown is the Score Transparency Requirement payload (US23, PRD 6.5).
//
// Every component is returned together with the collapsed FinalClaimScore, so
// the final number is never shown without access to its inputs.
type ScoreBreakdown struct {
	Reach              *float64 `json:"reach"`               // R, 0-100
	Velocity           *float64 `json:"velocity"`            // V, 0-100
	Falseness          *float64 `json:"falseness"`           // F, 0-100
	Harm               *float64 `json:"harm"`                // H, 0-100
	EmotionalIntensity *float64 `json:"emotional_intensity"` // EI supporting side, 0-100
	// EmotionalIntensityOpposing is display-only and never enters the score
	// (PRD 6.4.6 / US24).
	EmotionalIntensityOpposing *float64 `json:"emotional_intensity_opposing"`

	HarmBreakdown HarmBreakdown `json:"harm_breakdown"`

	ClaimScore      *float64 `json:"claim_score"`       // composite, pre-discount, 0-100
	NPR             *float64 `json:"npr"`               // 0-1, nil when dormant
	DiscountFactor  *float64 `json:"discount_factor"`   // 0.5-1, nil when dormant
	FinalClaimScore *float64 `json:"final_claim_score"` // 0-100, the ranking value

	IsDormant bool            `json:"is_dormant"`
	Note      string          `json:"note,omitempty"`
	Weights   scoring.Weights `json:"weights"`
}

// PolicyRef is a policy correlated with a claim (US12 many-to-many, US20
// one-to-many).
type PolicyRef struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	Source        string     `json:"source"` // cis = registered via F2, ai = created by the AI service
	AIPolicyID    *string    `json:"ai_policy_id,omitempty"`
	Status        *string    `json:"status,omitempty"` // rolled_out | not_rolled_out
	RolledOutDate *time.Time `json:"rolled_out_date,omitempty"`
	HasDocument   bool       `json:"has_document"`
}

// AccountRef is one row of the Top 5 Accounts panel (US12).
//
// Ranked over the Supporting-side cluster, matching the Reach parameter's
// scope in PRD 6.1.1.
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

// ActivityContent is the cached AI-generated Debunk or Prebunk draft
// (US12, US20). It is generated once by the AI service and served from cache;
// viewing a claim never triggers a new generation.
type ActivityContent struct {
	Type        string     `json:"type"` // debunk | prebunk
	Content     *string    `json:"content"`
	GeneratedAt *time.Time `json:"generated_at"`
	Available   bool       `json:"available"`
	// Debunk carries the same content pre-split into three blocks, for a
	// frontend that renders them as distinct labelled sections rather than one
	// paragraph. `content` remains the copyable single block.
	Debunk *DebunkBlocks `json:"debunk,omitempty"`
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

// ClaimDetail is the claim detail page payload (US12, US20).
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
}

// Statement is one source post backing a claim (US12).
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

// ClaimSection is one of the two F1 sections, S1 or S2.
type ClaimSection struct {
	Section     string      `json:"section"` // S1 | S2
	Label       string      `json:"label"`
	ClaimType   string      `json:"claim_type"`
	SortedBy    string      `json:"sorted_by"`
	TotalInPool int64       `json:"total_in_pool"`
	Claims      []ClaimCard `json:"claims"`
}

// RepositoryResponse is the whole F1 Claim Repository Bank page in one call.
//
// Both sections are always present regardless of the selected status tab: per
// US1, the status filter narrows claims *within* each section and never hides a
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

// ConfirmHarmRequest is the body of PUT /api/v1/claims/:id/harm/confirm
// (Flow 4).
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

// ScorePoint is one bucket of the F3 score history chart.
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
