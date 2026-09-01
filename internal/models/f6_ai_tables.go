package models

import (
	"time"

	"github.com/google/uuid"
)

// AI-owned tables added for PRD v1.5. READ ONLY here, exactly like every other
// model in ai_tables.go: they are provisioned by the AI service and never
// passed to AutoMigrate. See docs/sql/02_f6_reference_schema.sql.

// AIClaimDebunkSegment is one audience-segment-specific Debunk recommendation
// (PRD v1.5, US12).
//
// v1.4 and earlier stored a single generic draft in claims.activity_content.
// v1.5 requires the AI to identify the audience segments most exposed to a
// claim and generate one tailored copy recommendation per segment. That is a
// one-to-many relation, so it cannot live in a column on claims; it is its own
// AI-owned table, written once at claim creation and cached exactly like
// activity_content — viewing a claim never triggers a generation.
//
// The table is optional: on a deployment where the AI service has not shipped
// v1.5 yet, the detail page falls back to the single cached draft rather than
// failing. See ClaimRepository.ListDebunkSegments.
type AIClaimDebunkSegment struct {
	ID      uuid.UUID `gorm:"column:id;type:uuid;primaryKey"`
	ClaimID uuid.UUID `gorm:"column:claim_id;type:uuid"`
	// SegmentName labels the card in the UI ("Commuters", "Small business
	// owners"). US12 requires every variant to be visibly attributed to its
	// segment; a variant with no segment name would read as the generic draft
	// v1.5 exists to remove.
	SegmentName string `gorm:"column:segment_name"`
	// SegmentRationale is why this segment was identified — the framing or
	// concern the copy addresses.
	SegmentRationale *string `gorm:"column:segment_rationale"`
	Content          string  `gorm:"column:content"`
	// Rank orders the cards, most-exposed segment first.
	Rank        int       `gorm:"column:rank"`
	GeneratedAt time.Time `gorm:"column:generated_at"`
}

// TableName pins the AI-owned table name.
func (AIClaimDebunkSegment) TableName() string { return "claim_debunk_segments" }
