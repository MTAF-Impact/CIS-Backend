package scoring

// The Indonesia Climate Sentiment Index (PRD v1.5, Section 6.6), which powers
// F6 / O1.
//
// Unlike the claim-level parameters in scoring.go — computed and stored by the
// AI service, presented here — CSI is an aggregate over data the AI service
// does not roll up, so this backend does compute it. The formulas therefore
// live here, next to the weights they mirror, rather than inside the service
// that serves the page: PRD 6.5's transparency requirement applies to CSI too,
// and the one place the constants are written must be the one place they are
// applied.

// CSI component weights (PRD 6.6). Baseline sentiment and inverted risk load
// are weighted equally, so a calm-sounding but dangerous conversation cannot
// score as healthy on tone alone.
const (
	WeightBCS      = 0.5
	WeightRiskLoad = 0.5
)

// CSIRiskThreshold is the minimum FinalClaimScore a claim needs before it
// contributes to RiskLoad (PRD 6.6.2, recommended default 50). Below it, a
// claim is noise rather than a genuine city risk signal.
const CSIRiskThreshold = 50.0

// CSIMinimumVolume is the "minimum activity threshold" of PRD 6.6.3: below this
// many climate conversation items in the window, O1 shows "Insufficient Data"
// rather than implying a falsely calm score from low engagement.
const CSIMinimumVolume = 100

// CSIWindowDays is the 7-day rolling average PRD 6.6.3 fixes the headline
// figure to, chosen there to keep a single viral event from swinging the index.
const CSIWindowDays = 7

// CSIMomentumLagHours is the offset of the comparison window behind the
// headline one, giving the 24-48h direction-of-change indicator (PRD 6.6.3).
const CSIMomentumLagHours = 24

// BCS returns the Baseline Climate Sentiment, in [-1,+1] (PRD 6.6.1).
//
// Zero total volume returns 0: with nothing measured there is no sentiment to
// report, and callers gate on CSIMinimumVolume before ever displaying it.
func BCS(positiveVolume, negativeVolume, totalVolume int64) float64 {
	if totalVolume <= 0 {
		return 0
	}
	return float64(positiveVolume-negativeVolume) / float64(totalVolume)
}

// BCSNormalized rescales BCS from [-1,+1] onto [0,100] (PRD 6.6.1).
func BCSNormalized(bcs float64) float64 {
	return Clamp((bcs + 1) / 2 * 100)
}

// RiskLoad returns the volume-weighted burden of serious claims on the
// conversation (PRD 6.6.2).
//
// weightedScoreSum is Σ(FinalClaimScore_i × Volume_i) over claims scoring at or
// above CSIRiskThreshold; totalVolume is the same denominator BCS uses. The
// result is clamped to [0,100]: it is not mathematically bounded above, since
// scores can be weighted by a volume that is a subset of the denominator, and
// PRD 6.3's "still assert the bound" instruction applies with more force here
// than to the parameters where it is guaranteed.
func RiskLoad(weightedScoreSum float64, totalVolume int64) float64 {
	if totalVolume <= 0 {
		return 0
	}
	return Clamp(weightedScoreSum / float64(totalVolume))
}

// CSI combines the two components into the 0-100 index (PRD 6.6).
//
// RiskLoad is inverted because it reads "higher = worse"; inverting aligns its
// direction with BCS_normalized so the index consistently reads
// "higher = healthier".
func CSI(bcsNormalized, riskLoad float64) float64 {
	return Clamp(bcsNormalized*WeightBCS + (MaxScore-riskLoad)*WeightRiskLoad)
}

// CSI health bands, used for the red/amber/green gauge in US68.
const (
	CSIBandRisky   = "risky"
	CSIBandWatch   = "watch"
	CSIBandHealthy = "healthy"
)

// CSIBand names the gauge colour band for a score.
//
// The PRD specifies a red/amber/green banding without giving cut points; these
// split the scale into equal thirds, which is the assumption the API documents
// rather than hides.
func CSIBand(csi float64) string {
	switch {
	case csi < 100.0/3:
		return CSIBandRisky
	case csi < 200.0/3:
		return CSIBandWatch
	default:
		return CSIBandHealthy
	}
}

// FormulaSummary is the plain-language explanation behind the US23 info-tooltip
// on the Score Breakdown panel.
//
// It is served rather than hard-coded in the frontend so the words and the
// weights can never drift apart: both are generated from the constants above.
const FormulaSummary = "FinalClaimScore combines five parameters — Reach (15%), Velocity (15%), " +
	"Falseness Confidence (30%), Harm Severity (30%) and Emotional Intensity (10%) — into a " +
	"0-100 ClaimScore, then discounts it by how much the public is already pushing back. " +
	"Harm Severity is itself a weighted blend of Public Safety (35%), Institutional Trust (30%), " +
	"Economic (20%) and Policy Disruption (15%). The pushback discount never removes more than " +
	"half the score, so a heavily contested claim is de-prioritised but never hidden."
