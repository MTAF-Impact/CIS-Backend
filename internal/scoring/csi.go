package scoring

// The Indonesia Climate Sentiment Index, which powers the Overview page's
// gauge.
//
// Unlike the claim-level parameters in scoring.go — computed and stored by the
// AI service, presented here — CSI is an aggregate over data the AI service
// does not roll up, so this backend does compute it. The formulas therefore
// live here, next to the parameters they consume, rather than inside the
// service that serves the page: the arithmetic must stay explainable, and the
// one place it is written must be the one place it is applied.
//
// Every tunable in the index is an admin-configurable value (models.ConfigParams),
// so it arrives as a CSIParams rather than as a package constant.

// CSIParams is the configured shape of the index.
type CSIParams struct {
	// WeightBCS and WeightRiskLoad must sum to 1.00; the setting write path
	// enforces that, and this package assumes it.
	WeightBCS      float64
	WeightRiskLoad float64
	// RiskThreshold is the minimum FinalClaimScore a claim needs before it
	// contributes to RiskLoad. It is derived from the global alert
	// threshold rather than entered separately, so "elevated risk" means the
	// same thing on the Alert page and on this gauge.
	RiskThreshold float64
	// MinimumVolume is the minimum activity threshold: below this many
	// climate conversation items in the window, the gauge shows "Insufficient
	// Data" rather than implying a falsely calm score from low engagement.
	MinimumVolume int64
	// WindowDays is the rolling average behind the headline figure, chosen to
	// keep a single viral event from swinging the index.
	WindowDays int
	// MomentumLagHours is the offset of the comparison window behind the
	// headline one, giving the direction-of-change indicator.
	MomentumLagHours int
	// BandRiskyCeiling and BandWatchCeiling are the red/amber and amber/green
	// cut points of the gauge; the defaults split the scale into equal thirds.
	BandRiskyCeiling float64
	BandWatchCeiling float64
}

// BCS returns the Baseline Climate Sentiment, in [-1,+1].
//
// Zero total volume returns 0: with nothing measured there is no sentiment to
// report, and callers gate on MinimumVolume before ever displaying it.
func BCS(positiveVolume, negativeVolume, totalVolume int64) float64 {
	if totalVolume <= 0 {
		return 0
	}
	return float64(positiveVolume-negativeVolume) / float64(totalVolume)
}

// BCSNormalized rescales BCS from [-1,+1] onto [0,100].
func BCSNormalized(bcs float64) float64 {
	return Clamp((bcs + 1) / 2 * 100)
}

// RiskLoad returns the volume-weighted burden of serious claims on the
// conversation.
//
// weightedScoreSum is Σ(FinalClaimScore_i × Volume_i) over claims scoring at or
// above the configured RiskThreshold; totalVolume is the same denominator BCS
// uses. The result is clamped to [0,100]: it is not mathematically bounded
// above, since scores can be weighted by a volume that is a subset of the
// denominator, so the bound has to be asserted here explicitly rather than
// assumed as it is for the parameters where it is guaranteed.
func RiskLoad(weightedScoreSum float64, totalVolume int64) float64 {
	if totalVolume <= 0 {
		return 0
	}
	return Clamp(weightedScoreSum / float64(totalVolume))
}

// Index combines the two components into the 0-100 figure.
//
// RiskLoad is inverted because it reads "higher = worse"; inverting aligns its
// direction with BCS_normalized so the index consistently reads
// "higher = healthier".
func (p CSIParams) Index(bcsNormalized, riskLoad float64) float64 {
	return Clamp(bcsNormalized*p.WeightBCS + (MaxScore-riskLoad)*p.WeightRiskLoad)
}

// CSI health bands, used for the red/amber/green gauge.
const (
	CSIBandRisky   = "risky"
	CSIBandWatch   = "watch"
	CSIBandHealthy = "healthy"
)

// Band names the gauge colour band for a score, under the configured cut
// points.
func (p CSIParams) Band(csi float64) string {
	switch {
	case csi < p.BandRiskyCeiling:
		return CSIBandRisky
	case csi < p.BandWatchCeiling:
		return CSIBandWatch
	default:
		return CSIBandHealthy
	}
}
