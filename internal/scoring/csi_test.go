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
		// PRD 6.6.1 gives no value for an empty window; callers gate on
		// CSIMinimumVolume long before this, so it must simply not divide by zero.
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
	// PRD 6.6.2's ratio is not mathematically capped at 100: Volume_i is a
	// subset of the denominator, but a claim scoring 100 across most of the
	// conversation drives the quotient above it. 6.3's "still assert the bound"
	// applies with more force here than to the parameters where it is
	// guaranteed.
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

func TestCSIInvertsRiskLoad(t *testing.T) {
	// Higher must read as healthier: a calm-sounding conversation carrying a
	// heavy risk load must not score as healthy on tone alone (PRD 6.6.3).
	calmButRisky := CSI(90, 80)
	calmAndClean := CSI(90, 0)
	if calmButRisky >= calmAndClean {
		t.Errorf("risk load did not lower the index: %v >= %v", calmButRisky, calmAndClean)
	}
	if want := 0.5*90 + 0.5*(100-80); math.Abs(calmButRisky-want) > epsilon {
		t.Errorf("CSI = %v, want %v", calmButRisky, want)
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
		if got := CSIBand(tc.csi); got != tc.want {
			t.Errorf("CSIBand(%v) = %q, want %q", tc.csi, got, tc.want)
		}
	}
}
