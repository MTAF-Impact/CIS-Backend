// Package scoring encodes the PRD Section 6 Claim Scoring System as it is
// presented by this API.
//
// The AI service computes and stores every value; this backend never
// recalculates or writes a score. What lives here is the presentation contract:
//
//   - the configured weights, so the UI can explain the ranking (PRD 6.3, 6.2.4);
//   - defensive bounds checks, because PRD 6.3 and 6.4.4 both instruct
//     implementations to "still assert the bound" even though the formulas are
//     mathematically guaranteed to stay in range;
//   - the US25 dormancy rules that decide when NPR and the discount must be
//     presented as not-applicable rather than as a number.
//
// # Weights are values, not constants
//
// The weights below are the PRD's defaults, and they are what this package
// falls back to. The live values are admin-configurable through F4 and stored
// in cis_settings (see models.ConfigParams), so every function that needs them
// takes them as an argument rather than reading a package constant. That is
// what stops the API explaining a ranking with one set of numbers while the AI
// service computed it with another.
package scoring

import (
	"fmt"
	"math"
)

// Score bounds shared by every parameter and by the composite scores.
const (
	MinScore = 0.0
	MaxScore = 100.0
)

// Discount bounds (PRD 6.4.4). With the default gamma of 0.5, even total
// pushback reduces a score by at most half.
const (
	MinDiscountFactor = 0.5
	MaxDiscountFactor = 1.0
)

// Clamp constrains a 0-100 parameter to its documented range.
func Clamp(v float64) float64 {
	return math.Min(math.Max(v, MinScore), MaxScore)
}

// ClampPtr applies Clamp through a nullable score, preserving nil.
func ClampPtr(v *float64) *float64 {
	if v == nil {
		return nil
	}
	clamped := Clamp(*v)
	return &clamped
}

// ClampRatio constrains NPR to its native [0,1] range (PRD 6.4.3).
func ClampRatio(v *float64) *float64 {
	if v == nil {
		return nil
	}
	clamped := math.Min(math.Max(*v, 0), 1)
	return &clamped
}

// ClampDiscount constrains DiscountFactor to its floor and 1 (PRD 6.4.4).
//
// The floor is 1 - gamma: at the default gamma of 0.5 that is 0.5, but an
// operator who lowers gamma to 0.3 makes 0.7 the strongest possible discount,
// and clamping to a hardcoded 0.5 would then admit values the configuration
// says are impossible.
func ClampDiscount(v *float64, gamma float64) *float64 {
	if v == nil {
		return nil
	}
	floor := math.Max(MaxDiscountFactor-gamma, 0)
	clamped := math.Min(math.Max(*v, floor), MaxDiscountFactor)
	return &clamped
}

// Weights describes the weighting of the composite score, returned alongside
// every breakdown so the ranking is explainable in the UI (PRD 6.5).
type Weights struct {
	Reach              float64 `json:"reach"`
	Velocity           float64 `json:"velocity"`
	Falseness          float64 `json:"falseness"`
	Harm               float64 `json:"harm"`
	EmotionalIntensity float64 `json:"emotional_intensity"`
}

// HarmWeights describes the PRD 6.2.4 sub-weights.
type HarmWeights struct {
	PublicSafety       float64 `json:"public_safety"`
	InstitutionalTrust float64 `json:"institutional_trust"`
	Economic           float64 `json:"economic"`
	PolicyDisruption   float64 `json:"policy_disruption"`
}

// FormulaSummary is the plain-language explanation behind the US23 info-tooltip
// on the Score Breakdown panel.
//
// It is generated from the live weights rather than written out, and served
// rather than hardcoded in the frontend, so the words and the numbers can never
// drift apart — including after an admin retunes them in F4.
func FormulaSummary(w Weights, h HarmWeights, gamma float64) string {
	return fmt.Sprintf(
		"FinalClaimScore combines five parameters — Reach (%s), Velocity (%s), Falseness Confidence (%s), "+
			"Harm Severity (%s) and Emotional Intensity (%s) — into a 0-100 ClaimScore, then discounts it by "+
			"how much the public is already pushing back. Harm Severity is itself a weighted blend of "+
			"Public Safety (%s), Institutional Trust (%s), Economic (%s) and Policy Disruption (%s). "+
			"The pushback discount never removes more than %s of the score, so a heavily contested claim "+
			"is de-prioritised but never hidden.",
		asPercent(w.Reach), asPercent(w.Velocity), asPercent(w.Falseness),
		asPercent(w.Harm), asPercent(w.EmotionalIntensity),
		asPercent(h.PublicSafety), asPercent(h.InstitutionalTrust),
		asPercent(h.Economic), asPercent(h.PolicyDisruption),
		asPercent(gamma),
	)
}

// asPercent renders a 0-1 weight as a whole-number percentage where it is one,
// and to one decimal place otherwise, so 0.15 reads "15%" rather than "15.0%".
func asPercent(w float64) string {
	pct := w * 100
	if math.Abs(pct-math.Round(pct)) < 0.05 {
		return fmt.Sprintf("%.0f%%", pct)
	}
	return fmt.Sprintf("%.1f%%", pct)
}

// DormancyNote explains why NPR and the discount are unavailable for a dormant
// claim (PRD 6.4.7 / US25). A dormant claim has no supporting or opposing
// volume in the rolling window, so it is flagged rather than discounted — its
// priority must never be lowered on the basis of missing data.
const DormancyNote = "No supporting or opposing volume in the rolling window, so this claim is flagged dormant " +
	"rather than discounted. NPR and DiscountFactor are not applicable (PRD 6.4.7)."
