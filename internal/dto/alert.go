package dto

import "time"

// Threshold statuses shown in the F3 watchlist (US29).
const (
	ThresholdOver  = "over_threshold"
	ThresholdUnder = "under_threshold"
)

// AlertRow is one row of the [C3] watchlist table (US29).
type AlertRow struct {
	// ID is the claim id, which the table's ID column displays.
	ID             string    `json:"id"`
	AlertID        string    `json:"alert_id"`
	ClaimStatement string    `json:"claim_statement"`
	ClaimCreatedAt time.Time `json:"claim_created_at"`
	AddedAt        time.Time `json:"added_at"`
	// ChartVisible backs the "Chart" checkbox column (US28).
	ChartVisible bool      `json:"chart_visible"`
	Topic        *TopicRef `json:"topic,omitempty"`
	ReviewStatus string    `json:"review_status"`

	FinalClaimScore *float64 `json:"final_claim_score"`
	// ThresholdStatus is derived by comparing FinalClaimScore against the F4
	// global threshold (US29, US32).
	ThresholdStatus string  `json:"threshold_status"`
	Threshold       float64 `json:"threshold"`
	IsDormant       bool    `json:"is_dormant"`

	// JustCrossed drives the US29/US71 row highlight: true while this claim's
	// Over/Under status has flipped since the reader last opened F3. It is
	// per-reader, so one operator acknowledging a crossing does not clear a
	// colleague's highlight.
	JustCrossed bool `json:"just_crossed"`
	// CrossedDirection is "up" (below -> above) or "down" (above -> below),
	// omitted when the claim has never crossed. Retained after acknowledgment
	// so the table can still show what last happened to a claim.
	CrossedDirection *string    `json:"crossed_direction,omitempty"`
	CrossedAt        *time.Time `json:"crossed_at,omitempty"`
}

// AlertNotifications is the US71 payload behind the sidebar counter badge.
type AlertNotifications struct {
	// UnacknowledgedCount is the badge number: watched claims that have crossed
	// the threshold since this user last opened F3.
	UnacknowledgedCount int64 `json:"unacknowledged_count"`
	// AcknowledgedAt is when this user last opened F3, null if never.
	AcknowledgedAt *time.Time `json:"acknowledged_at"`
	Threshold      float64    `json:"threshold"`
	// Crossings names the claims behind the count, newest first, so the badge
	// can be expanded into something readable rather than only counted.
	Crossings []AlertRow `json:"crossings"`
}

// AddAlertRequest is the body of POST /api/v1/alerts, sent after the user
// confirms the bell-icon dialog (US14).
type AddAlertRequest struct {
	ClaimID string `json:"claim_id" validate:"required,uuid"`
}

// SetChartVisibilityRequest is the body of
// PATCH /api/v1/alerts/:claimId/chart (US28).
type SetChartVisibilityRequest struct {
	Visible *bool `json:"visible" validate:"required"`
}

// AlertMutationResponse confirms an add, remove, or chart toggle.
type AlertMutationResponse struct {
	ClaimID      string     `json:"claim_id"`
	OnWatchlist  bool       `json:"on_watchlist"`
	ChartVisible bool       `json:"chart_visible"`
	AddedAt      *time.Time `json:"added_at,omitempty"`
}

// ChartSeries is one claim's line on the [C1] chart, with its [C2] legend entry.
type ChartSeries struct {
	ClaimID        string       `json:"claim_id"`
	ClaimStatement string       `json:"claim_statement"`
	Topic          *TopicRef    `json:"topic,omitempty"`
	Points         []ScorePoint `json:"points"`
}

// ChartResponse is the F3 chart payload (US27, US28).
//
// It contains only claims the user has explicitly checked in [C3]; with none
// checked, Series is empty and the UI shows its empty state.
type ChartResponse struct {
	Granularity string        `json:"granularity"`
	Threshold   float64       `json:"threshold"`
	YAxisMin    float64       `json:"y_axis_min"`
	YAxisMax    float64       `json:"y_axis_max"`
	Series      []ChartSeries `json:"series"`
}
