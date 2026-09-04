package scoring

import (
	"math"
	"testing"
)

const epsilon = 1e-9

func TestBCSAndNormalization(t *testing.T) {
	cases := []struct {
		name                      string
		positive, negative, total int64
		wantBCS                   float64
		wantNormalized            float64
	}{
		{"entirely positive", 100, 0, 100, 1, 100},
		{"entirely negative", 0, 100, 100, -1, 0},
		{"balanced", 50, 50, 100, 0, 50},
		{"neutral majority", 10, 5, 100, 0.05, 52.5},
		// An empty window has no defined value; callers gate on MinimumVolume
		// long before this, so it must simply not divide by zero.
		{"no conversation", 0, 0, 0, 0, 50},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bcs := BCS(tc.positive, tc.negative, tc.total)
			if math.Abs(bcs-tc.wantBCS) > epsilon {
				t.Errorf("BCS = %v, want %v", bcs, tc.wantBCS)
			}
			if got := BCSNormalized(bcs); math.Abs(got-tc.wantNormalized) > epsilon {
				t.Errorf("BCSNormalized = %v, want %v", got, tc.wantNormalized)
			}
		})
	}
}

func TestRiskLoadIsBounded(t *testing.T) {
	// The ratio is not mathematically capped at 100: Volume_i is a subset of
	// the denominator, but a claim scoring 100 across most of the conversation
	// drives the quotient above it, so the bound has to be asserted explicitly
	// rather than relying on it falling out of the formula.
	if got := RiskLoad(1_000_000, 100); got != MaxScore {
		t.Errorf("RiskLoad over-range = %v, want %v", got, MaxScore)
	}
	if got := RiskLoad(4000, 100); math.Abs(got-40) > epsilon {
		t.Errorf("RiskLoad = %v, want 40", got)
	}
	if got := RiskLoad(500, 0); got != 0 {
		t.Errorf("RiskLoad with no conversation = %v, want 0", got)
	}
}

// defaultParams mirrors the seeded configuration, so these tests assert fixed,
// known-good numbers rather than whatever the registry happens to hold.
var defaultParams = CSIParams{
	WeightBCS:        0.5,
	WeightRiskLoad:   0.5,
	RiskThreshold:    70,
	MinimumVolume:    100,
	WindowDays:       7,
	MomentumLagHours: 24,
	BandRiskyCeiling: 33.33,
	BandWatchCeiling: 66.67,
}

func TestCSIInvertsRiskLoad(t *testing.T) {
	// Higher must read as healthier: a calm-sounding conversation carrying a
	// heavy risk load must not score as healthy on tone alone.
	calmButRisky := defaultParams.Index(90, 80)
	calmAndClean := defaultParams.Index(90, 0)
	if calmButRisky >= calmAndClean {
		t.Errorf("risk load did not lower the index: %v >= %v", calmButRisky, calmAndClean)
	}
	if want := 0.5*90 + 0.5*(100-80); math.Abs(calmButRisky-want) > epsilon {
		t.Errorf("CSI = %v, want %v", calmButRisky, want)
	}
}

// TestCSIHonoursConfiguredWeights covers the case the equal default hides: with
// both halves at 0.5, a bug that swapped them would still produce the right
// number.
func TestCSIHonoursConfiguredWeights(t *testing.T) {
	toneOnly := defaultParams
	toneOnly.WeightBCS = 1
	toneOnly.WeightRiskLoad = 0

	if got := toneOnly.Index(90, 80); math.Abs(got-90) > epsilon {
		t.Errorf("tone-only index = %v, want 90", got)
	}
}

func TestCSIBand(t *testing.T) {
	cases := []struct {
		csi  float64
		want string
	}{
		{0, CSIBandRisky},
		{33, CSIBandRisky},
		{34, CSIBandWatch},
		{66, CSIBandWatch},
		{67, CSIBandHealthy},
		{100, CSIBandHealthy},
	}

	for _, tc := range cases {
		if got := defaultParams.Band(tc.csi); got != tc.want {
			t.Errorf("Band(%v) = %q, want %q", tc.csi, got, tc.want)
		}
	}
}
