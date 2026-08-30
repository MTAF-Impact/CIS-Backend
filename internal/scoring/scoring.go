// Package scoring encodes the PRD Section 6 Claim Scoring System as it is
// presented by this API.
//
// The AI service computes and stores every value; this backend never
// recalculates or writes a score. What lives here is the presentation contract:
//
//   - the published weights, so the UI can explain the ranking (PRD 6.3, 6.2.4);
//   - defensive bounds checks, because PRD 6.3 and 6.4.4 both instruct
//     implementations to "still assert the bound" even though the formulas are
//     mathematically guaranteed to stay in range;
//   - the US25 dormancy rules that decide when NPR and the discount must be
//     presented as not-applicable rather than as a number.
package scoring

import "math"

// Composite Claim Score weights (PRD 6.3). They sum to exactly 1.00, which is
// what guarantees ClaimScore lands in [0,100].
const (
	WeightReach              = 0.15
	WeightVelocity           = 0.15
	WeightFalseness          = 0.30
	WeightHarm               = 0.30
	WeightEmotionalIntensity = 0.10
)

// Harm Severity sub-weights (PRD 6.2.4). PolicyDisruption is deliberately
// weighted lowest: scoring criticism of a government's own policy as "harm"
// carries inherent bias risk.
const (
	WeightHarmPublicSafety       = 0.35
	WeightHarmInstitutionalTrust = 0.30
	WeightHarmEconomic           = 0.20
	WeightHarmPolicyDisruption   = 0.15
)

// Score bounds shared by every parameter and by the composite scores.
const (
	MinScore = 0.0
	MaxScore = 100.0
)

// Discount bounds (PRD 6.4.4). With the recommended gamma of 0.5, even total
// pushback reduces a score by at most half.
const (
	MinDiscountFactor = 0.5
	MaxDiscountFactor = 1.0
	RecommendedGamma  = 0.5
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

// ClampDiscount constrains DiscountFactor to [0.5,1] (PRD 6.4.4).
func ClampDiscount(v *float64) *float64 {
	if v == nil {
		return nil
	}
	clamped := math.Min(math.Max(*v, MinDiscountFactor), MaxDiscountFactor)
	return &clamped
}

// Weights describes the published weighting of the composite score, returned
// alongside every breakdown so the ranking is explainable in the UI (PRD 6.5).
type Weights struct {
	Reach              float64 `json:"reach"`
	Velocity           float64 `json:"velocity"`
	Falseness          float64 `json:"falseness"`
	Harm               float64 `json:"harm"`
	EmotionalIntensity float64 `json:"emotional_intensity"`
}

// PublishedWeights returns the PRD 6.3 composite weights.
func PublishedWeights() Weights {
	return Weights{
		Reach:              WeightReach,
		Velocity:           WeightVelocity,
		Falseness:          WeightFalseness,
		Harm:               WeightHarm,
		EmotionalIntensity: WeightEmotionalIntensity,
	}
}

// HarmWeights describes the PRD 6.2.4 sub-weights.
type HarmWeights struct {
	PublicSafety       float64 `json:"public_safety"`
	InstitutionalTrust float64 `json:"institutional_trust"`
	Economic           float64 `json:"economic"`
	PolicyDisruption   float64 `json:"policy_disruption"`
}

// PublishedHarmWeights returns the PRD 6.2.4 harm sub-weights.
func PublishedHarmWeights() HarmWeights {
	return HarmWeights{
		PublicSafety:       WeightHarmPublicSafety,
		InstitutionalTrust: WeightHarmInstitutionalTrust,
		Economic:           WeightHarmEconomic,
		PolicyDisruption:   WeightHarmPolicyDisruption,
	}
}

// DormancyNote explains why NPR and the discount are unavailable for a dormant
// claim (PRD 6.4.7 / US25). A dormant claim has no supporting or opposing
// volume in the rolling window, so it is flagged rather than discounted — its
// priority must never be lowered on the basis of missing data.
const DormancyNote = "No supporting or opposing volume in the rolling window, so this claim is flagged dormant " +
	"rather than discounted. NPR and DiscountFactor are not applicable (PRD 6.4.7)."
