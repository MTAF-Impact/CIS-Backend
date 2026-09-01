package dto

import "time"

// F6 — Overview (PRD v1.5, Section 11). One page, three sections: O1 the
// above/below-threshold ratio and Climate Sentiment Index, O2 the topic
// treemap, O3 the hot-policy leaderboard.

// CityScope names the city every metric on the page is scoped to (US65).
type CityScope struct {
	Name     string `json:"name"`
	Province string `json:"province"`
	Timezone string `json:"timezone"`
	// Partitioned is false when the AI service does not yet tag content with a
	// city, in which case the selection labels this instance rather than
	// filtering it. Surfaced rather than hidden: a leadership page must not
	// imply a city breakdown the data cannot support.
	Partitioned bool `json:"partitioned"`
}

// Climate Sentiment Index availability states (PRD 6.6.3).
const (
	// CSIStatusOK means the index was computed over sufficient volume.
	CSIStatusOK = "ok"
	// CSIStatusInsufficientData is PRD 6.6.3's minimum activity threshold:
	// below it, an index would read as calm when it only means quiet.
	CSIStatusInsufficientData = "insufficient_data"
	// CSIStatusUnavailable means the AI service has not provisioned the
	// sentiment data the index is computed from.
	CSIStatusUnavailable = "unavailable"
)

// ThresholdRatio is the O1 above/below-threshold split (US67).
//
// It counts every Existing/Generic claim regardless of review status: the ratio
// describes the information environment, not the team's triage queue.
type ThresholdRatio struct {
	Above        int64   `json:"above"`
	Below        int64   `json:"below"`
	Total        int64   `json:"total"`
	AbovePercent float64 `json:"above_percent"`
	Threshold    float64 `json:"threshold"`
}

// ConversationVolume is the sentiment split of the climate conversation behind
// BCS (PRD 6.6.1).
type ConversationVolume struct {
	Total    int64 `json:"total"`
	Positive int64 `json:"positive"`
	Negative int64 `json:"negative"`
	Neutral  int64 `json:"neutral"`
}

// SentimentIndex is the O1 gauge (US67, US68, PRD 6.6).
//
// Every component is returned alongside the headline number, for the same
// reason PRD 6.5 requires it of claim scores: a collapsed index a reviewer
// cannot decompose is not defensible in a public-sector context. US68's
// click-through breakdown renders BCSNormalized and RiskLoad as the two bars.
type SentimentIndex struct {
	Status string `json:"status"` // ok | insufficient_data | unavailable
	// Reason explains a non-ok status in words the UI can show directly.
	Reason string `json:"reason,omitempty"`

	Score *float64 `json:"score"`          // 0-100, nil unless status is ok
	Band  *string  `json:"band,omitempty"` // risky | watch | healthy

	BCS           *float64 `json:"bcs"`            // -1..+1
	BCSNormalized *float64 `json:"bcs_normalized"` // 0-100
	RiskLoad      *float64 `json:"risk_load"`      // 0-100, higher is worse

	// Momentum is the change against a window lagged by 24h, giving US67's
	// direction-of-change indicator. Nil when the lagged window is itself below
	// the minimum volume.
	Momentum          *float64 `json:"momentum"`
	MomentumDirection *string  `json:"momentum_direction,omitempty"` // up | down | flat

	Volume ConversationVolume `json:"volume"`

	WindowStart time.Time `json:"window_start"`
	WindowEnd   time.Time `json:"window_end"`
	// The published parameters behind the number, so the tooltip in US68 can be
	// generated from the same constants the score was.
	WindowDays    int     `json:"window_days"`
	MinimumVolume int64   `json:"minimum_volume"`
	RiskThreshold float64 `json:"risk_threshold"`
	WeightBCS     float64 `json:"weight_bcs"`
	WeightRisk    float64 `json:"weight_risk_load"`
}

// TopicBox is one rectangle of the O2 treemap (US69).
type TopicBox struct {
	Topic               TopicRef `json:"topic"`
	ClaimCount          int64    `json:"claim_count"`
	AboveThresholdCount int64    `json:"above_threshold_count"`
	AverageScore        *float64 `json:"average_score"`
	// BoxSize is the 0-100 area weight. US69 left the formula unspecified; the
	// PRD's own proposed default is used and published here so the treemap can
	// be explained: each input is normalised against the largest topic in the
	// current set, then the two are averaged 50/50, mirroring the CSI weighting.
	BoxSize float64 `json:"box_size"`
}

// TopicOverviewDetail is the O2 click-through modal (US69).
type TopicOverviewDetail struct {
	Topic               TopicRef `json:"topic"`
	ClaimCount          int64    `json:"claim_count"`
	AboveThresholdCount int64    `json:"above_threshold_count"`
	BelowThresholdCount int64    `json:"below_threshold_count"`
	// AboveUnderRatio is above divided by below, null when nothing is below
	// threshold and the ratio would be undefined.
	AboveUnderRatio *float64 `json:"above_under_ratio"`
	AverageScore    *float64 `json:"average_score"`

	// Month-on-month movement of the average score, from the AI service's
	// score history. Null when there is not enough history on both sides of the
	// comparison to make the percentage mean anything.
	AverageScoreMoMPercent *float64 `json:"average_score_mom_percent"`
	MoMDirection           *string  `json:"mom_direction,omitempty"` // up | down | flat
	CurrentMonthAverage    *float64 `json:"current_month_average"`
	PreviousMonthAverage   *float64 `json:"previous_month_average"`

	Threshold float64 `json:"threshold"`
}

// HotPolicy is one row of the O3 leaderboard (US70).
type HotPolicy struct {
	Rank                int       `json:"rank"`
	Policy              PolicyRef `json:"policy"`
	ClaimCount          int64     `json:"claim_count"`
	AboveThresholdCount int64     `json:"above_threshold_count"`
	AverageScore        *float64  `json:"average_score"`
	// Score is the same 0-100 combined metric that sizes the O2 treemap, per
	// US70's "ranked by the same combined metric as O2".
	Score float64 `json:"score"`
}

// OverviewResponse is the whole F6 page in one call, mirroring the F1
// repository endpoint: the three sections are read together on every load, and
// three round trips to render one screen buys nothing.
type OverviewResponse struct {
	City        CityScope `json:"city"`
	GeneratedAt time.Time `json:"generated_at"`

	ThresholdRatio ThresholdRatio `json:"threshold_ratio"` // O1a
	Sentiment      SentimentIndex `json:"sentiment"`       // O1b
	Topics         []TopicBox     `json:"topics"`          // O2
	Policies       []HotPolicy    `json:"policies"`        // O3
}
