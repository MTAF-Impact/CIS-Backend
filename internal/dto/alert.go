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
