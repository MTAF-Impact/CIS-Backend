package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Every table in this file is owned exclusively by this backend and carries the
// `cis_` prefix so it can never collide with a table the AI service creates.
// These are the only models passed to AutoMigrate.

// Claim review statuses (PRD US1, unified in v1.3 across both claim types).
const (
	ReviewStatusUnreviewed  = "unreviewed"
	ReviewStatusActive      = "active"
	ReviewStatusInactive    = "inactive"
	ReviewStatusActionTaken = "action_taken"
)

// ValidReviewStatuses lists every accepted claim review status.
var ValidReviewStatuses = []string{
	ReviewStatusUnreviewed,
	ReviewStatusActive,
	ReviewStatusInactive,
	ReviewStatusActionTaken,
}

// IsValidReviewStatus reports whether s is an accepted review status.
func IsValidReviewStatus(s string) bool {
	for _, v := range ValidReviewStatuses {
		if v == s {
			return true
		}
	}
	return false
}

// Policy rollout statuses (PRD US37, US41).
const (
	PolicyStatusNotRolledOut = "not_rolled_out"
	PolicyStatusRolledOut    = "rolled_out"
)

// AI matchmaking job states, driving the F2 "Processing" badge (PRD US42).
const (
	ProcessingPending    = "pending"
	ProcessingInProgress = "processing"
	ProcessingCompleted  = "completed"
	ProcessingFailed     = "failed"
	// ProcessingSkipped means no AI service is configured, so matchmaking was
	// never attempted. Distinct from failed so the UI does not show an error.
	ProcessingSkipped = "skipped"
)

// Settings keys stored in cis_settings.
const (
	SettingAlertThreshold      = "alert_threshold"        // PRD US32, 0-100
	SettingClaimsLastFetchedAt = "claims_last_fetched_at" // PRD US9/US33
)

// DefaultAlertThreshold is seeded when cis_settings has no threshold yet.
const DefaultAlertThreshold = 70.0

// CISUser is an operator account for the login flow.
//
// The PRD defines no user model, so this is intentionally minimal: there are no
// roles, and any authenticated user may use every endpoint including F4.
type CISUser struct {
	ID           uuid.UUID  `gorm:"column:id;type:uuid;primaryKey"`
	Email        string     `gorm:"column:email;type:varchar(255);not null;uniqueIndex:idx_cis_users_email"`
	PasswordHash string     `gorm:"column:password_hash;type:varchar(255);not null"`
	Name         string     `gorm:"column:name;type:varchar(255);not null"`
	LastLoginAt  *time.Time `gorm:"column:last_login_at"`
	CreatedAt    time.Time  `gorm:"column:created_at;not null"`
	UpdatedAt    time.Time  `gorm:"column:updated_at;not null"`
}

// TableName pins the backend-owned table name.
func (CISUser) TableName() string { return "cis_users" }

// BeforeCreate assigns a UUID, matching the AI schema's convention of
// application-generated ids (its tables declare no column defaults).
func (u *CISUser) BeforeCreate(*gorm.DB) error {
	if u.ID == uuid.Nil {
		u.ID = uuid.New()
	}
	return nil
}

// CISRefreshToken stores hashed refresh tokens so they can be rotated and
// revoked. The raw token is never persisted.
type CISRefreshToken struct {
	ID        uuid.UUID  `gorm:"column:id;type:uuid;primaryKey"`
	UserID    uuid.UUID  `gorm:"column:user_id;type:uuid;not null;index:idx_cis_refresh_tokens_user"`
	TokenHash string     `gorm:"column:token_hash;type:varchar(128);not null;uniqueIndex:idx_cis_refresh_tokens_hash"`
	ExpiresAt time.Time  `gorm:"column:expires_at;not null;index:idx_cis_refresh_tokens_expires"`
	RevokedAt *time.Time `gorm:"column:revoked_at"`
	CreatedAt time.Time  `gorm:"column:created_at;not null"`
}

// TableName pins the backend-owned table name.
func (CISRefreshToken) TableName() string { return "cis_refresh_tokens" }

// BeforeCreate assigns a UUID.
func (t *CISRefreshToken) BeforeCreate(*gorm.DB) error {
	if t.ID == uuid.Nil {
		t.ID = uuid.New()
	}
	return nil
}

// IsUsable reports whether the token is neither revoked nor expired.
func (t *CISRefreshToken) IsUsable(now time.Time) bool {
	return t.RevokedAt == nil && now.Before(t.ExpiresAt)
}

// CISPolicy is a public policy registered through F2.
//
// The backend owns this record end to end and never writes the AI service's
// `policies` table. AIPolicyID is a soft reference (no FK) filled in by the AI
// service once matchmaking completes, and is the join key used to resolve a
// policy's correlated claims.
type CISPolicy struct {
	ID          uuid.UUID `gorm:"column:id;type:uuid;primaryKey"`
	Name        string    `gorm:"column:name;type:varchar(500);not null;index:idx_cis_policies_name"`
	Description *string   `gorm:"column:description;type:text"`

	// RolledOutDate drives Status (US41). Stored as a date; the daily cron flips
	// Status once the date arrives.
	RolledOutDate time.Time `gorm:"column:rolled_out_date;type:date;not null;index:idx_cis_policies_rolled_out_date"`
	Status        string    `gorm:"column:status;type:varchar(32);not null;index:idx_cis_policies_status"`

	// Uploaded document (US40). PDF/Word only, no size cap.
	FileName      string `gorm:"column:file_name;type:varchar(500);not null"`
	FilePath      string `gorm:"column:file_path;type:varchar(1000);not null"`
	FileMimeType  string `gorm:"column:file_mime_type;type:varchar(255);not null"`
	FileSizeBytes int64  `gorm:"column:file_size_bytes;not null"`

	// AI matchmaking state (US42).
	AIPolicyID         *uuid.UUID `gorm:"column:ai_policy_id;type:uuid;index:idx_cis_policies_ai_policy_id"`
	ProcessingStatus   string     `gorm:"column:processing_status;type:varchar(32);not null;index:idx_cis_policies_processing_status"`
	ProcessingError    *string    `gorm:"column:processing_error;type:text"`
	ProcessingAttempts int        `gorm:"column:processing_attempts;not null;default:0"`
	ProcessedAt        *time.Time `gorm:"column:processed_at"`

	CreatedBy *uuid.UUID `gorm:"column:created_by;type:uuid"`
	CreatedAt time.Time  `gorm:"column:created_at;not null;index:idx_cis_policies_created_at"`
	UpdatedAt time.Time  `gorm:"column:updated_at;not null"`
}

// TableName pins the backend-owned table name.
func (CISPolicy) TableName() string { return "cis_policies" }

// BeforeCreate assigns a UUID.
func (p *CISPolicy) BeforeCreate(*gorm.DB) error {
	if p.ID == uuid.Nil {
		p.ID = uuid.New()
	}
	return nil
}

// DeriveStatus returns the rollout status implied by the policy's date as of
// now (PRD US41).
func DeriveStatus(rolledOutDate, now time.Time) string {
	// Compare on calendar days in UTC: a policy rolling out "today" counts as
	// rolled out, per US41's "on or before the current date".
	d := time.Date(rolledOutDate.Year(), rolledOutDate.Month(), rolledOutDate.Day(), 0, 0, 0, 0, time.UTC)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	if d.After(today) {
		return PolicyStatusNotRolledOut
	}
	return PolicyStatusRolledOut
}

// CISClaimReview records a human's status decision for a claim (US10, US18).
//
// This is an overlay: the AI service's own `claims.status` is left untouched, so
// a pipeline re-run can never silently overwrite a reviewer's decision. Reads
// resolve a claim's status as COALESCE(review.status, 'unreviewed').
type CISClaimReview struct {
	ID         uuid.UUID  `gorm:"column:id;type:uuid;primaryKey"`
	ClaimID    uuid.UUID  `gorm:"column:claim_id;type:uuid;not null;uniqueIndex:idx_cis_claim_reviews_claim"`
	Status     string     `gorm:"column:status;type:varchar(32);not null;index:idx_cis_claim_reviews_status"`
	Notes      *string    `gorm:"column:notes;type:text"`
	ReviewedBy *uuid.UUID `gorm:"column:reviewed_by;type:uuid"`
	ReviewedAt time.Time  `gorm:"column:reviewed_at;not null"`
	CreatedAt  time.Time  `gorm:"column:created_at;not null"`
	UpdatedAt  time.Time  `gorm:"column:updated_at;not null"`
}

// TableName pins the backend-owned table name.
func (CISClaimReview) TableName() string { return "cis_claim_reviews" }

// BeforeCreate assigns a UUID.
func (r *CISClaimReview) BeforeCreate(*gorm.DB) error {
	if r.ID == uuid.Nil {
		r.ID = uuid.New()
	}
	return nil
}

// Threshold-crossing directions (PRD v1.5, US71).
const (
	// ThresholdStatusUnknown is the state of a freshly watched claim, before
	// any evaluation has run. US71 fires "only for a genuine transition", so
	// the first evaluation records a baseline and never counts as a crossing.
	ThresholdStatusUnknown = ""
	ThresholdStatusOver    = "over"
	ThresholdStatusUnder   = "under"

	// CrossingDirectionUp is below -> above threshold; Down is the reverse.
	// US71 requires notifying on both.
	CrossingDirectionUp   = "up"
	CrossingDirectionDown = "down"
)

// CISClaimAlert is one row of the F3 watchlist (US14, US29, US30).
//
// Rows are only ever created through the F1 bell-icon confirmation flow, and
// only for Existing/Generic claims (US26).
type CISClaimAlert struct {
	ID      uuid.UUID `gorm:"column:id;type:uuid;primaryKey"`
	ClaimID uuid.UUID `gorm:"column:claim_id;type:uuid;not null;uniqueIndex:idx_cis_claim_alerts_claim"`
	// ChartVisible backs the [C3] "Chart" checkbox that decides which claims the
	// [C1] line chart and [C2] key render (US28).
	ChartVisible bool       `gorm:"column:chart_visible;not null;default:false"`
	AddedBy      *uuid.UUID `gorm:"column:added_by;type:uuid"`
	AddedAt      time.Time  `gorm:"column:added_at;not null;index:idx_cis_claim_alerts_added_at"`

	// Threshold-crossing detection (PRD v1.5, US71).
	//
	// LastThresholdStatus is the Over/Under status recorded at the previous
	// evaluation. A crossing is the transition between two evaluations, so the
	// prior status has to be stored somewhere: FinalClaimScore alone only says
	// where the claim is now, never that it just moved. Empty means "not yet
	// evaluated" and seeds the baseline without notifying.
	LastThresholdStatus string     `gorm:"column:last_threshold_status;type:varchar(16);not null;default:''"`
	CrossedAt           *time.Time `gorm:"column:crossed_at;index:idx_cis_claim_alerts_crossed_at"`
	CrossedDirection    *string    `gorm:"column:crossed_direction;type:varchar(8)"`

	CreatedAt time.Time `gorm:"column:created_at;not null"`
	UpdatedAt time.Time `gorm:"column:updated_at;not null"`
}

// TableName pins the backend-owned table name.
func (CISClaimAlert) TableName() string { return "cis_claim_alerts" }

// BeforeCreate assigns a UUID.
func (a *CISClaimAlert) BeforeCreate(*gorm.DB) error {
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	return nil
}

// CISAlertAcknowledgement records when a user last opened F3 (PRD v1.5, US71).
//
// US71 clears both the sidebar counter and the row highlight "once the user
// opens the F3 page". That is a per-person acknowledgment: one operator opening
// the page must not silently clear a colleague's badge, so this is keyed by
// user rather than being a single global timestamp. A crossing counts as
// unacknowledged for a user when crossed_at is after their acknowledged_at.
type CISAlertAcknowledgement struct {
	UserID         uuid.UUID `gorm:"column:user_id;type:uuid;primaryKey"`
	AcknowledgedAt time.Time `gorm:"column:acknowledged_at;not null"`
	UpdatedAt      time.Time `gorm:"column:updated_at;not null"`
}

// TableName pins the backend-owned table name.
func (CISAlertAcknowledgement) TableName() string { return "cis_alert_acknowledgements" }

// CISClaimHarmEdit is the audit trail for a human override of a claim's Harm
// sub-scores (PRD v1.5, US23).
//
// US23 requires edited sub-scores to be "tagged as human-overridden (vs.
// AI-original), recording the editing user and timestamp". The values
// themselves live on the AI-owned claims table, which this backend never
// writes — harm_human_confirmed is the flag it sets, through the AI service,
// and a boolean carries neither who nor when nor what changed. This table is
// append-only and holds all three.
type CISClaimHarmEdit struct {
	ID      uuid.UUID `gorm:"column:id;type:uuid;primaryKey"`
	ClaimID uuid.UUID `gorm:"column:claim_id;type:uuid;not null;index:idx_cis_claim_harm_edits_claim,priority:1"`

	// Previous values, captured before the override, so the AI's original
	// classification is recoverable from the audit trail alone.
	PreviousPublicSafety       *float64 `gorm:"column:previous_public_safety"`
	PreviousInstitutionalTrust *float64 `gorm:"column:previous_institutional_trust"`
	PreviousEconomic           *float64 `gorm:"column:previous_economic"`
	PreviousPolicyDisruption   *float64 `gorm:"column:previous_policy_disruption"`
	PreviousHarmScore          *float64 `gorm:"column:previous_harm_score"`

	// Submitted values. A nil field means the reviewer left that sub-score
	// alone, which US23 treats as confirming the AI's value rather than
	// changing it.
	PublicSafety       *float64 `gorm:"column:public_safety"`
	InstitutionalTrust *float64 `gorm:"column:institutional_trust"`
	Economic           *float64 `gorm:"column:economic"`
	PolicyDisruption   *float64 `gorm:"column:policy_disruption"`

	EditedBy *uuid.UUID `gorm:"column:edited_by;type:uuid"`
	EditedAt time.Time  `gorm:"column:edited_at;not null;index:idx_cis_claim_harm_edits_claim,priority:2"`
}

// TableName pins the backend-owned table name.
func (CISClaimHarmEdit) TableName() string { return "cis_claim_harm_edits" }

// BeforeCreate assigns a UUID.
func (e *CISClaimHarmEdit) BeforeCreate(*gorm.DB) error {
	if e.ID == uuid.Nil {
		e.ID = uuid.New()
	}
	return nil
}

// CISClaimScoreSnapshot is a point-in-time copy of a claim's Section 6 scores.
//
// The AI service only stores a claim's *current* score, but the F3 chart plots
// FinalClaimScore over time (US27). A cron job periodically copies scores here
// to build that history without ever writing to the AI's tables.
type CISClaimScoreSnapshot struct {
	ID      uuid.UUID `gorm:"column:id;type:uuid;primaryKey"`
	ClaimID uuid.UUID `gorm:"column:claim_id;type:uuid;not null;index:idx_cis_snapshots_claim_captured,priority:1"`

	ReachScore                 *float64 `gorm:"column:reach_score"`
	VelocityScore              *float64 `gorm:"column:velocity_score"`
	FalsenessScore             *float64 `gorm:"column:falseness_score"`
	HarmScore                  *float64 `gorm:"column:harm_score"`
	EmotionalIntensityScore    *float64 `gorm:"column:emotional_intensity_score"`
	EmotionalIntensityOpposing *float64 `gorm:"column:emotional_intensity_opposing"`
	ClaimScore                 *float64 `gorm:"column:claim_score"`
	NPR                        *float64 `gorm:"column:npr"`
	DiscountFactor             *float64 `gorm:"column:discount_factor"`
	FinalClaimScore            *float64 `gorm:"column:final_claim_score"`
	IsDormant                  bool     `gorm:"column:is_dormant;not null;default:false"`

	CapturedAt time.Time `gorm:"column:captured_at;not null;index:idx_cis_snapshots_claim_captured,priority:2"`
	CreatedAt  time.Time `gorm:"column:created_at;not null"`
}

// TableName pins the backend-owned table name.
func (CISClaimScoreSnapshot) TableName() string { return "cis_claim_score_snapshots" }

// BeforeCreate assigns a UUID.
func (s *CISClaimScoreSnapshot) BeforeCreate(*gorm.DB) error {
	if s.ID == uuid.Nil {
		s.ID = uuid.New()
	}
	return nil
}

// CISSetting is a single global configuration value (F4).
type CISSetting struct {
	ID          uuid.UUID  `gorm:"column:id;type:uuid;primaryKey"`
	Key         string     `gorm:"column:key;type:varchar(128);not null;uniqueIndex:idx_cis_settings_key"`
	Value       string     `gorm:"column:value;type:text;not null"`
	ValueType   string     `gorm:"column:value_type;type:varchar(32);not null"` // number | string | timestamp | boolean
	Description string     `gorm:"column:description;type:text"`
	UpdatedBy   *uuid.UUID `gorm:"column:updated_by;type:uuid"`
	CreatedAt   time.Time  `gorm:"column:created_at;not null"`
	UpdatedAt   time.Time  `gorm:"column:updated_at;not null"`
}

// TableName pins the backend-owned table name.
func (CISSetting) TableName() string { return "cis_settings" }

// BeforeCreate assigns a UUID.
func (s *CISSetting) BeforeCreate(*gorm.DB) error {
	if s.ID == uuid.Nil {
		s.ID = uuid.New()
	}
	return nil
}
