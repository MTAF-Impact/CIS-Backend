// Package models holds the GORM structs for both halves of the shared database.
//
// # Table ownership
//
// This database is shared with a separately-developed AI service. The rule is
// absolute:
//
//   - Tables in THIS file are owned by the AI service. The backend only ever
//     SELECTs from them. They are never inserted, updated, deleted, or passed
//     to AutoMigrate.
//   - Tables in the cis_*.go files are owned by the backend. It has exclusive
//     write access and AutoMigrate manages their shape.
//
// # Why `embedding` columns are absent
//
// Every AI table with semantic search has an `embedding` column of pgvector's
// `vector` type. Go/GORM has no native representation for it, so the field is
// deliberately omitted rather than mapped. This guarantees no query ever
// selects it and — critically — that AutoMigrate could never recreate one of
// these tables without it. See migrate.go.
package models

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// Canonical claim types. The PRD calls these Existing/Generic and
// Non-Existing/Synthetic; the AI service writes one of the aliases below into
// claims.claim_type.
const (
	ClaimTypeExisting    = "existing"
	ClaimTypeNonExisting = "non_existing"
)

// Claim type aliases accepted from the AI service. Queries filter with
// `claim_type IN (...)` over these sets so the backend keeps working whichever
// vocabulary the AI pipeline settles on. See docs/AI-INTEGRATION.md.
var (
	ExistingClaimTypeValues    = []string{"existing", "generic", "existing_claim", "generic_claim"}
	NonExistingClaimTypeValues = []string{"non_existing", "synthetic", "non-existing", "predicted", "synthetic_claim", "non_existing_claim"}
)

// NormalizeClaimType maps a raw claims.claim_type value onto one of the two
// canonical types. Unrecognized values fall back to non-existing, since an
// unscored claim is the safer default to present.
func NormalizeClaimType(raw string) string {
	needle := strings.ToLower(strings.TrimSpace(raw))
	for _, v := range ExistingClaimTypeValues {
		if needle == v {
			return ClaimTypeExisting
		}
	}
	return ClaimTypeNonExisting
}

// ClaimTypeValues returns the DB values matching a canonical claim type, for
// use in an IN clause.
func ClaimTypeValues(canonical string) []string {
	if canonical == ClaimTypeExisting {
		return ExistingClaimTypeValues
	}
	return NonExistingClaimTypeValues
}

// Content stance values, written by the AI service on content_items.stance.
//
// The PRD's "Positive Statements" / "Negative Statements" lists (US12) map onto
// these: Positive = supporting, Negative = opposing. Neutral is excluded from
// both, mirroring the NPR definition in PRD 6.4.2.
const (
	StanceSupporting = "supporting"
	StanceOpposing   = "opposing"
	StanceNeutral    = "neutral"
)

// AIClaim is the AI service's `claims` table. READ ONLY.
type AIClaim struct {
	ID             uuid.UUID  `gorm:"column:id;type:uuid;primaryKey"`
	ClaimType      string     `gorm:"column:claim_type"`
	ClaimStatement string     `gorm:"column:claim_statement"`
	TopicID        uuid.UUID  `gorm:"column:topic_id;type:uuid"`
	Status         string     `gorm:"column:status"`
	PolicyID       *uuid.UUID `gorm:"column:policy_id;type:uuid"`
	FirstCaughtAt  time.Time  `gorm:"column:first_caught_at"`

	// PRD Section 6 parameters. All are computed and written by the AI service;
	// the backend reads and displays them but never recomputes or stores them.
	ReachScore                 *float64 `gorm:"column:reach_score"`              // R
	VelocityScore              *float64 `gorm:"column:velocity_score"`           // V
	FalsenessScore             *float64 `gorm:"column:falseness_score"`          // F
	HarmScore                  *float64 `gorm:"column:harm_score"`               // H
	HarmPublicSafety           *float64 `gorm:"column:harm_public_safety"`       // H sub-score, weight 0.35
	HarmInstitutionalTrust     *float64 `gorm:"column:harm_institutional_trust"` // H sub-score, weight 0.30
	HarmEconomic               *float64 `gorm:"column:harm_economic"`            // H sub-score, weight 0.20
	HarmPolicyDisruption       *float64 `gorm:"column:harm_policy_disruption"`   // H sub-score, weight 0.15
	HarmHumanConfirmed         bool     `gorm:"column:harm_human_confirmed"`
	EmotionalIntensityScore    *float64 `gorm:"column:emotional_intensity_score"`    // EI (supporting side)
	EmotionalIntensityOpposing *float64 `gorm:"column:emotional_intensity_opposing"` // EI_opposing, diagnostic only
	ClaimScore                 *float64 `gorm:"column:claim_score"`                  // composite, pre-discount
	NPR                        *float64 `gorm:"column:npr"`                          // net pushback ratio, 0-1
	DiscountFactor             *float64 `gorm:"column:discount_factor"`              // 0.5-1
	FinalClaimScore            *float64 `gorm:"column:final_claim_score"`            // ranking value, 0-100
	IsDormant                  bool     `gorm:"column:is_dormant"`

	// Debunk/Prebunk draft, generated once by the AI service and cached here.
	// US12/US20 require the backend to serve this without re-calling the AI.
	ActivityContent     *string    `gorm:"column:activity_content"`
	ActivityGeneratedAt *time.Time `gorm:"column:activity_generated_at"`

	// The Truth Sandwich's three blocks, split out of ActivityContent by the AI
	// service specifically so the frontend can render three labelled sections
	// instead of one paragraph. Existing claims only; NULL on Synthetic ones.
	DebunkCoreFact       *string `gorm:"column:debunk_core_fact"`
	DebunkNuancedFlag    *string `gorm:"column:debunk_nuanced_flag"`
	DebunkReiteratedFact *string `gorm:"column:debunk_reiterated_fact"`

	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

// TableName pins the AI-owned table name.
func (AIClaim) TableName() string { return "claims" }

// AITopic is the AI service's `topics` table. READ ONLY.
type AITopic struct {
	ID          uuid.UUID `gorm:"column:id;type:uuid;primaryKey"`
	Name        string    `gorm:"column:name"`
	Description *string   `gorm:"column:description"`
	CreatedAt   time.Time `gorm:"column:created_at"`
	UpdatedAt   time.Time `gorm:"column:updated_at"`
}

// TableName pins the AI-owned table name.
func (AITopic) TableName() string { return "topics" }

// AIPolicy is the AI service's `policies` table. READ ONLY.
//
// The backend never writes here. Policies created through F2 live in
// cis_policies and reference this table's id via CISPolicy.AIPolicyID once the
// AI service reports back from matchmaking (US42).
type AIPolicy struct {
	ID          uuid.UUID `gorm:"column:id;type:uuid;primaryKey"`
	Title       string    `gorm:"column:title"`
	Description *string   `gorm:"column:description"`
	CreatedAt   time.Time `gorm:"column:created_at"`
}

// TableName pins the AI-owned table name.
func (AIPolicy) TableName() string { return "policies" }

// AIClaimPolicy is the AI service's `claim_policies` join table, carrying the
// many-to-many Existing-claim <-> policy relation from US12/US39. READ ONLY.
type AIClaimPolicy struct {
	ClaimID  uuid.UUID `gorm:"column:claim_id;type:uuid;primaryKey"`
	PolicyID uuid.UUID `gorm:"column:policy_id;type:uuid;primaryKey"`
}

// TableName pins the AI-owned table name.
func (AIClaimPolicy) TableName() string { return "claim_policies" }

// AIContentItem is the AI service's `content_items` table: the individual
// posts clustered into a claim. READ ONLY.
type AIContentItem struct {
	ID                    uuid.UUID  `gorm:"column:id;type:uuid;primaryKey"`
	Text                  string     `gorm:"column:text"`
	Source                string     `gorm:"column:source"`
	AuthorID              *string    `gorm:"column:author_id"`
	Location              *string    `gorm:"column:location"`
	OutrageScore          *float64   `gorm:"column:outrage_score"`
	MoralFoundation       *string    `gorm:"column:moral_foundation"`
	ExtractedClaim        *string    `gorm:"column:extracted_claim"`
	UnderlyingGrievance   *string    `gorm:"column:underlying_grievance"`
	Stance                *string    `gorm:"column:stance"`
	Impressions           *int       `gorm:"column:impressions"`
	PositiveReactionCount *int       `gorm:"column:positive_reaction_count"`
	NegativeReactionCount *int       `gorm:"column:negative_reaction_count"`
	ClaimID               *uuid.UUID `gorm:"column:claim_id;type:uuid"`
	CreatedAt             time.Time  `gorm:"column:created_at"`
}

// TableName pins the AI-owned table name.
func (AIContentItem) TableName() string { return "content_items" }

// AITopicVolumeBucket is the AI service's `topic_volume_buckets` table, the
// time-series backing the Velocity parameter's baseline. READ ONLY.
type AITopicVolumeBucket struct {
	ID               uuid.UUID `gorm:"column:id;type:uuid;primaryKey"`
	TopicID          uuid.UUID `gorm:"column:topic_id;type:uuid"`
	BucketStart      time.Time `gorm:"column:bucket_start"`
	SupportingVolume int       `gorm:"column:supporting_volume"`
}

// TableName pins the AI-owned table name.
func (AITopicVolumeBucket) TableName() string { return "topic_volume_buckets" }

// AIClaimScoreSnapshot is the AI service's `claim_score_snapshots` table: an
// append-only history of final_claim_score. READ ONLY.
//
// The AI service appends a row every time it rescores a claim — on clustering,
// on a harm confirmation, and on POST /claims/rescore — for every claim
// touched. That is event-driven and covers every claim, where the backend's own
// cis_claim_score_snapshots is sampled hourly and only for watched claims. The
// F3 chart therefore reads both and merges them; see SnapshotRepository.Series.
//
// It carries no claim_score column, only the final value.
type AIClaimScoreSnapshot struct {
	ID              uuid.UUID `gorm:"column:id;type:uuid;primaryKey"`
	ClaimID         uuid.UUID `gorm:"column:claim_id;type:uuid"`
	FinalClaimScore float64   `gorm:"column:final_claim_score"`
	RecordedAt      time.Time `gorm:"column:recorded_at"`
}

// TableName pins the AI-owned table name.
func (AIClaimScoreSnapshot) TableName() string { return "claim_score_snapshots" }

// AIFaultLine is the AI service's `fault_lines` table. READ ONLY.
type AIFaultLine struct {
	ID             uuid.UUID `gorm:"column:id;type:uuid;primaryKey"`
	CommunityName  string    `gorm:"column:community_name"`
	GrievanceTheme string    `gorm:"column:grievance_theme"`
	Description    *string   `gorm:"column:description"`
	CreatedAt      time.Time `gorm:"column:created_at"`
}

// TableName pins the AI-owned table name.
func (AIFaultLine) TableName() string { return "fault_lines" }

// AIOfficialSource is the AI service's `official_sources` table: the verified
// corpus behind the Falseness Confidence parameter (PRD 6.2.3). READ ONLY.
type AIOfficialSource struct {
	ID        uuid.UUID `gorm:"column:id;type:uuid;primaryKey"`
	Title     string    `gorm:"column:title"`
	Content   string    `gorm:"column:content"`
	SourceURL *string   `gorm:"column:source_url"`
	CreatedAt time.Time `gorm:"column:created_at"`
}

// TableName pins the AI-owned table name.
func (AIOfficialSource) TableName() string { return "official_sources" }
