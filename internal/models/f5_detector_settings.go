package models

import (
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// CISDetectorSettings is the coordinated-network detector's governed parameter set.
//
// # Why one row with typed columns rather than ~24 cis_settings keys
//
// cis_settings is a flat key/value store and can physically hold these, but
// two of the constraints here are CROSS-FIELD — the fusion weights must sum to
// 1.00, and the scheduled cadence must not exceed half the detection window —
// and a per-key setter cannot see the other keys to check them. Typed columns
// also put every range check in Go where it is testable, matching how
// SettingService.SetAlertThreshold already validates 0-100.
//
// # The two rules that make this table more than configuration
//
//  1. Every change is versioned with the acting user and a timestamp
//     (CISSettingHistory) — the only history table anywhere in the schema.
//  2. Changing a parameter must NEVER retroactively alter a stored detection.
//     Every run copies the whole set into detection_run.parameters_json when it
//     executes, so a report generated months later states the configuration
//     that actually produced it. Nothing reads this table to interpret a past
//     run.
//
// Defaults and ranges are reproduced exactly from the governing spec. Where a
// parameter has no default or range at all, that is called out on the field.
type CISDetectorSettings struct {
	// ID is fixed to DetectorSettingsID: there is exactly one row, because the
	// configuration is global the same way the alert threshold is.
	ID uuid.UUID `gorm:"column:id;type:uuid;primaryKey"`

	// --- Window & bins ---

	// WindowDays is W, the rolling detection window.
	WindowDays int `gorm:"column:window_days;not null"`
	// BinWidthSeconds is delta, the temporal-synchrony bin width.
	BinWidthSeconds int `gorm:"column:bin_width_seconds;not null"`

	// --- Statistics ---

	// NullModelAlpha is alpha: a pair is retained only if its observed
	// co-occurrence exceeds the (1 - alpha) quantile of the null model.
	NullModelAlpha float64 `gorm:"column:null_model_alpha;not null"`
	// DupThreshold is tau_dup, the MinHash near-duplicate Jaccard cutoff.
	DupThreshold float64 `gorm:"column:dup_threshold;not null"`
	// SemThreshold is tau_sem, the multilingual paraphrase cosine cutoff. It
	// needs separate validation on Bahasa Indonesia and code-mixed text before
	// launch — a threshold tuned on English will be miscalibrated.
	SemThreshold float64 `gorm:"column:sem_threshold;not null"`
	// MinPostLength is L_min in normalised characters. Below it, "Setuju!!" and
	// "tolak!" are identical across thousands of unrelated real people.
	MinPostLength int `gorm:"column:min_post_length;not null"`

	// --- Graph construction ---

	// EdgeThreshold is theta_edge, the fused edge-weight floor.
	EdgeThreshold float64 `gorm:"column:edge_threshold;not null"`
	// MinSignalFamilies is the multi-signal rule: how many distinct families
	// must independently reach 0.25 on an edge. This is the pipeline's primary
	// false-positive control, and its range starts at 2 — one is never
	// permissible.
	MinSignalFamilies int `gorm:"column:min_signal_families;not null"`
	// KCore is k, the k-core reduction depth.
	KCore int `gorm:"column:k_core;not null"`
	// LeidenResolution is gamma_res.
	LeidenResolution float64 `gorm:"column:leiden_resolution;not null"`
	// MinClusterSize is N_min. Clusters below it are never surfaced.
	MinClusterSize int `gorm:"column:min_cluster_size;not null"`
	// MinInternalDensity is rho_min.
	MinInternalDensity float64 `gorm:"column:min_internal_density;not null"`

	// --- Signal fusion weights ---
	//
	// These must sum to 1.00. Each is individually in range at the defaults and
	// at many other combinations, which is exactly what makes the sum easy to
	// break by editing one of them.
	BetaTime   float64 `gorm:"column:beta_time;not null"`
	BetaText   float64 `gorm:"column:beta_text;not null"`
	BetaAmp    float64 `gorm:"column:beta_amp;not null"`
	BetaMeta   float64 `gorm:"column:beta_meta;not null"`
	BetaStruct float64 `gorm:"column:beta_struct;not null"`

	// --- Provenance ---

	// ProvenanceHalfLifeHours is h, the creation-time proximity half-life. It
	// must be admin-configurable; without it the w_meta creation-time
	// sub-signal is hardcoded.
	ProvenanceHalfLifeHours int `gorm:"column:provenance_half_life_hours;not null"`

	// --- Claim-relevance gate ---

	// AnchorShare is mu_anchor: the share of members needing >= 2 posts in the
	// claim cluster. Rejects clusters assembled from accounts that touched the
	// claim once in passing.
	AnchorShare float64 `gorm:"column:anchor_share;not null"`
	// MinClaimPosts is P_min. Below it the sample cannot support an inference —
	// deliberately consistent with the NPR reliability floor used elsewhere.
	MinClaimPosts int `gorm:"column:min_claim_posts;not null"`
	// MinLinkStrength is omega_min: the overlap_ratio floor establishing that
	// the claim is a substantive part of what the cluster does.
	MinLinkStrength float64 `gorm:"column:min_link_strength;not null"`

	// --- Confidence banding ---

	HighScoreCutoff   float64 `gorm:"column:high_score_cutoff;not null"`
	HighBreadthCutoff int     `gorm:"column:high_breadth_cutoff;not null"`
	MediumScoreCutoff float64 `gorm:"column:medium_score_cutoff;not null"`
	// MediumBreadthCutoff is the floor under the whole guard: a high composite
	// score with SignalBreadth = 1 is the characteristic shape of a false
	// positive, not of a campaign, so this may never drop to 1.
	MediumBreadthCutoff int `gorm:"column:medium_breadth_cutoff;not null"`

	// --- Execution ---

	// CadenceHours is the scheduled run interval. Its lower bound of 1 hour is
	// a scope boundary, not a preference: real-time (sub-hourly) detection is
	// explicitly out of scope for this version.
	CadenceHours int `gorm:"column:cadence_hours;not null"`
	// CandidateCap is A_max.
	CandidateCap int `gorm:"column:candidate_cap;not null"`
	// RecurrenceThreshold is the member-set Jaccard at which a new cluster is
	// recorded as a recurrence of a stored fingerprint.
	RecurrenceThreshold float64 `gorm:"column:recurrence_threshold;not null"`

	// VelocityTriggerThreshold fires an unscheduled run when a claim's V
	// crosses it — a growth spike is exactly when a network is most likely
	// present and most detectable.
	//
	// This parameter must be configurable but has no documented default or
	// range: V is on a 0-100 scale, so the value is at least well defined. The
	// default below is a backend placeholder pending a PM ruling, deliberately
	// set to the same number as the seeded alert threshold rather than an
	// invented one.
	VelocityTriggerThreshold float64 `gorm:"column:velocity_trigger_threshold;not null"`

	// VelocityTriggerEnabled lets the trigger be switched off entirely while
	// the threshold above is unsettled, without having to pick a number so high
	// it never fires.
	VelocityTriggerEnabled bool `gorm:"column:velocity_trigger_enabled;not null"`

	UpdatedBy *uuid.UUID `gorm:"column:updated_by;type:uuid"`
	CreatedAt time.Time  `gorm:"column:created_at;not null"`
	UpdatedAt time.Time  `gorm:"column:updated_at;not null"`
}

// TableName pins the backend-owned table name.
func (CISDetectorSettings) TableName() string { return "cis_detector_settings" }

// DetectorSettingsID is the fixed primary key of the single settings row.
var DetectorSettingsID = uuid.MustParse("00000000-0000-0000-0000-0000000000f5")

// BeforeCreate pins the singleton id.
func (s *CISDetectorSettings) BeforeCreate(*gorm.DB) error {
	if s.ID == uuid.Nil {
		s.ID = DetectorSettingsID
	}
	return nil
}

// AllowlistSuppressionShare is the cutoff: a network whose membership is at
// least 60% allowlisted is suppressed entirely and logged as an allowlist
// hit.
//
// Deliberately NOT configurable: it is the one threshold whose whole purpose
// is to protect civil society from the tool — a number that can be tuned
// down under pressure to make a detection "work" is not a safeguard.
const AllowlistSuppressionShare = 0.60

// DefaultDetectorSettings returns the detector's default parameter set.
func DefaultDetectorSettings() CISDetectorSettings {
	return CISDetectorSettings{
		ID:                       DetectorSettingsID,
		WindowDays:               7,
		BinWidthSeconds:          60,
		NullModelAlpha:           0.01,
		DupThreshold:             0.80,
		SemThreshold:             0.90,
		MinPostLength:            25,
		EdgeThreshold:            0.35,
		MinSignalFamilies:        2,
		KCore:                    3,
		LeidenResolution:         1.0,
		MinClusterSize:           5,
		MinInternalDensity:       0.30,
		BetaTime:                 0.30,
		BetaText:                 0.25,
		BetaAmp:                  0.20,
		BetaMeta:                 0.15,
		BetaStruct:               0.10,
		ProvenanceHalfLifeHours:  36,
		AnchorShare:              0.60,
		MinClaimPosts:            20,
		MinLinkStrength:          0.15,
		HighScoreCutoff:          70,
		HighBreadthCutoff:        3,
		MediumScoreCutoff:        55,
		MediumBreadthCutoff:      2,
		CadenceHours:             6,
		CandidateCap:             5000,
		RecurrenceThreshold:      0.50,
		VelocityTriggerThreshold: 70,
		VelocityTriggerEnabled:   false,
	}
}

// ParamRange documents one parameter's admissible interval for the API, so the
// frontend can render bounded inputs without duplicating them elsewhere.
type ParamRange struct {
	Key     string  `json:"key"`
	Label   string  `json:"label"`
	Symbol  string  `json:"symbol,omitempty"`
	Min     float64 `json:"min"`
	Max     float64 `json:"max"`
	Default float64 `json:"default"`
	Unit    string  `json:"unit,omitempty"`
	Integer bool    `json:"integer"`
	// Note carries a caveat the range alone does not express.
	Note string `json:"note,omitempty"`
}

// DetectorParamRanges is the full default parameter reference for the detector.
//
// It is data rather than scattered literals so the same source drives
// validation, the API's self-description, and the tests.
var DetectorParamRanges = []ParamRange{
	{Key: "window_days", Label: "Detection window", Symbol: "W", Min: 1, Max: 30, Default: 7, Unit: "days", Integer: true},
	{Key: "bin_width_seconds", Label: "Time bin width", Symbol: "δ", Min: 10, Max: 300, Default: 60, Unit: "seconds", Integer: true},
	{Key: "null_model_alpha", Label: "Null-model significance", Symbol: "α", Min: 0.001, Max: 0.05, Default: 0.01},
	{Key: "dup_threshold", Label: "Near-duplicate threshold", Symbol: "τ_dup", Min: 0.70, Max: 0.95, Default: 0.80},
	{Key: "sem_threshold", Label: "Semantic paraphrase threshold", Symbol: "τ_sem", Min: 0.80, Max: 0.98, Default: 0.90,
		Note: "Validate separately on Bahasa Indonesia and code-mixed text before launch."},
	{Key: "min_post_length", Label: "Minimum post length", Symbol: "L_min", Min: 10, Max: 100, Default: 25, Unit: "characters", Integer: true},
	{Key: "edge_threshold", Label: "Edge weight threshold", Symbol: "θ_edge", Min: 0.20, Max: 0.70, Default: 0.35},
	{Key: "min_signal_families", Label: "Minimum signal families per edge", Min: 2, Max: 3, Default: 2, Integer: true,
		Note: "The multi-signal rule. Never 1: synchrony alone is a timezone, duplication alone is a hashtag."},
	{Key: "k_core", Label: "k-core", Symbol: "k", Min: 2, Max: 5, Default: 3, Integer: true},
	{Key: "leiden_resolution", Label: "Leiden resolution", Symbol: "γ_res", Min: 0.5, Max: 2.0, Default: 1.0},
	{Key: "min_cluster_size", Label: "Minimum cluster size", Symbol: "N_min", Min: 3, Max: 20, Default: 5, Unit: "accounts", Integer: true},
	{Key: "min_internal_density", Label: "Minimum internal density", Symbol: "ρ_min", Min: 0.15, Max: 0.60, Default: 0.30},
	{Key: "provenance_half_life_hours", Label: "Provenance creation half-life", Symbol: "h", Min: 6, Max: 168, Default: 36, Unit: "hours", Integer: true},
	{Key: "anchor_share", Label: "Member anchoring share", Symbol: "μ_anchor", Min: 0.40, Max: 0.90, Default: 0.60},
	{Key: "min_claim_posts", Label: "Minimum claim-cluster posts", Symbol: "P_min", Min: 10, Max: 100, Default: 20, Unit: "posts", Integer: true},
	{Key: "min_link_strength", Label: "Minimum claim link strength", Symbol: "ω_min", Min: 0.05, Max: 0.50, Default: 0.15},
	{Key: "cadence_hours", Label: "Scheduled cadence", Min: 1, Max: 24, Default: 6, Unit: "hours", Integer: true,
		Note: "The 1-hour floor is a scope boundary: sub-hourly detection is out of scope."},
	{Key: "candidate_cap", Label: "Candidate cap", Symbol: "A_max", Min: 500, Max: 20000, Default: 5000, Unit: "accounts", Integer: true},
	{Key: "recurrence_threshold", Label: "Recurrence match threshold", Min: 0.30, Max: 0.80, Default: 0.50, Unit: "Jaccard"},

	// Not in the spec's default parameter table, but required to be configurable.
	{Key: "beta_time", Label: "Fusion weight — temporal synchrony", Symbol: "β_time", Min: 0, Max: 1, Default: 0.30, Note: "The five β must sum to 1.00."},
	{Key: "beta_text", Label: "Fusion weight — content duplication", Symbol: "β_text", Min: 0, Max: 1, Default: 0.25, Note: "The five β must sum to 1.00."},
	{Key: "beta_amp", Label: "Fusion weight — co-amplification", Symbol: "β_amp", Min: 0, Max: 1, Default: 0.20, Note: "The five β must sum to 1.00."},
	{Key: "beta_meta", Label: "Fusion weight — provenance", Symbol: "β_meta", Min: 0, Max: 1, Default: 0.15, Note: "The five β must sum to 1.00."},
	{Key: "beta_struct", Label: "Fusion weight — structural overlap", Symbol: "β_struct", Min: 0, Max: 1, Default: 0.10, Note: "The five β must sum to 1.00."},
	{Key: "high_score_cutoff", Label: "High confidence — score cutoff", Min: 0, Max: 100, Default: 70},
	{Key: "high_breadth_cutoff", Label: "High confidence — SignalBreadth cutoff", Min: 2, Max: 5, Default: 3, Integer: true},
	{Key: "medium_score_cutoff", Label: "Medium confidence — score cutoff", Min: 0, Max: 100, Default: 55},
	{Key: "medium_breadth_cutoff", Label: "Medium confidence — SignalBreadth cutoff", Min: 2, Max: 5, Default: 2, Integer: true,
		Note: "Never 1: a high composite with SignalBreadth = 1 is the characteristic shape of a false positive."},
	{Key: "velocity_trigger_threshold", Label: "Velocity trigger threshold", Min: 0, Max: 100, Default: 70,
		Note: "No stated default or range: the bounds are V's own 0-100 scale; the default is a placeholder pending a PM ruling."},
}

// betaSumTolerance absorbs float64 representation error. 0.30 + 0.25 + 0.20 +
// 0.15 + 0.10 is not exactly 1.0 in binary floating point, so an exact
// comparison would reject the defaults above.
const betaSumTolerance = 1e-9

// Validate applies the ranges above plus the constraints no single range can
// express, returning one error per offending field.
//
// The cross-field checks are the ones worth reading twice. Both are satisfied
// by the defaults and both are reachable using values that are individually
// legal, which is exactly what makes them easy to ship broken.
func (s CISDetectorSettings) Validate() map[string]string {
	errs := map[string]string{}

	checkInt := func(key string, v int) {
		r, ok := paramRange(key)
		if !ok {
			return
		}
		if float64(v) < r.Min || float64(v) > r.Max {
			errs[key] = fmt.Sprintf("must be between %d and %d", int(r.Min), int(r.Max))
		}
	}
	checkFloat := func(key string, v float64) {
		r, ok := paramRange(key)
		if !ok {
			return
		}
		if v < r.Min || v > r.Max {
			errs[key] = fmt.Sprintf("must be between %g and %g", r.Min, r.Max)
		}
	}

	checkInt("window_days", s.WindowDays)
	checkInt("bin_width_seconds", s.BinWidthSeconds)
	checkFloat("null_model_alpha", s.NullModelAlpha)
	checkFloat("dup_threshold", s.DupThreshold)
	checkFloat("sem_threshold", s.SemThreshold)
	checkInt("min_post_length", s.MinPostLength)
	checkFloat("edge_threshold", s.EdgeThreshold)
	checkInt("min_signal_families", s.MinSignalFamilies)
	checkInt("k_core", s.KCore)
	checkFloat("leiden_resolution", s.LeidenResolution)
	checkInt("min_cluster_size", s.MinClusterSize)
	checkFloat("min_internal_density", s.MinInternalDensity)
	checkFloat("beta_time", s.BetaTime)
	checkFloat("beta_text", s.BetaText)
	checkFloat("beta_amp", s.BetaAmp)
	checkFloat("beta_meta", s.BetaMeta)
	checkFloat("beta_struct", s.BetaStruct)
	checkInt("provenance_half_life_hours", s.ProvenanceHalfLifeHours)
	checkFloat("anchor_share", s.AnchorShare)
	checkInt("min_claim_posts", s.MinClaimPosts)
	checkFloat("min_link_strength", s.MinLinkStrength)
	checkFloat("high_score_cutoff", s.HighScoreCutoff)
	checkInt("high_breadth_cutoff", s.HighBreadthCutoff)
	checkFloat("medium_score_cutoff", s.MediumScoreCutoff)
	checkInt("medium_breadth_cutoff", s.MediumBreadthCutoff)
	checkInt("cadence_hours", s.CadenceHours)
	checkInt("candidate_cap", s.CandidateCap)
	checkFloat("recurrence_threshold", s.RecurrenceThreshold)
	checkFloat("velocity_trigger_threshold", s.VelocityTriggerThreshold)

	// Cross-field 1: the fusion weights must sum to 1.00.
	// w(i,j) = Σ β_k · w_k with Σ β_k = 1 is what keeps the fused edge weight
	// on the same [0,1] scale as θ_edge, so a sum of 0.9 silently makes every
	// edge weaker than it is and a sum of 1.1 makes every edge stronger.
	sum := s.BetaTime + s.BetaText + s.BetaAmp + s.BetaMeta + s.BetaStruct
	if math.Abs(sum-1.0) > betaSumTolerance {
		errs["beta_weights"] = fmt.Sprintf("the five signal fusion weights must sum to 1.00, got %.4f", sum)
	}

	// Cross-field 2: cadence <= W/2, which is how the 50% window overlap
	// requirement is actually enforced.
	//
	// With the defaults this is satisfied by a wide margin — a 7-day window
	// re-run every 6 hours overlaps by far more than half. But W and the
	// cadence are INDEPENDENTLY configurable (1-30 days and 1-24 h), so an
	// admin can legally set W = 1 day with a 24 h cadence, get 0% overlap, and
	// open a boundary blind spot every midnight: behaviour straddling the seam
	// is split across two runs and missed by both.
	if s.WindowDays > 0 && s.CadenceHours > 0 {
		maxCadence := float64(s.WindowDays) * 24.0 / 2.0
		if float64(s.CadenceHours) > maxCadence {
			errs["cadence_hours"] = fmt.Sprintf(
				"consecutive runs must overlap by 50%% of the window, so the cadence may not exceed %.0f hours for a %d-day window",
				maxCadence, s.WindowDays,
			)
		}
	}

	// Cross-field 3: High must be strictly harder to reach than Medium.
	// Nothing enforces this by construction, but banding where High is easier
	// than Medium would assign bands that contradict their own labels.
	if s.HighScoreCutoff < s.MediumScoreCutoff {
		errs["high_score_cutoff"] = "High confidence cannot require a lower score than Medium"
	}
	if s.HighBreadthCutoff < s.MediumBreadthCutoff {
		errs["high_breadth_cutoff"] = "High confidence cannot require less signal breadth than Medium"
	}

	return errs
}

func paramRange(key string) (ParamRange, bool) {
	for _, r := range DetectorParamRanges {
		if r.Key == key {
			return r, true
		}
	}
	return ParamRange{}, false
}

// Window returns W as a duration.
func (s CISDetectorSettings) Window() time.Duration {
	return time.Duration(s.WindowDays) * 24 * time.Hour
}

// Cadence returns the scheduled run interval as a duration.
func (s CISDetectorSettings) Cadence() time.Duration {
	return time.Duration(s.CadenceHours) * time.Hour
}

// BandFor returns the confidence band implied by a score and signal breadth
// under the configured cutoffs.
//
// The backend does not assign bands — the pipeline does, and the stored value
// is authoritative. This exists so the settings screen can show an admin what a
// cutoff change would mean, and so tests can assert the rule.
func (s CISDetectorSettings) BandFor(score float64, breadth int) string {
	switch {
	case score >= s.HighScoreCutoff && breadth >= s.HighBreadthCutoff:
		return ConfidenceHigh
	case score >= s.MediumScoreCutoff && breadth >= s.MediumBreadthCutoff:
		return ConfidenceMedium
	default:
		return ConfidenceLow
	}
}
