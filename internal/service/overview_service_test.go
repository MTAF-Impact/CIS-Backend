package service

import (
	"math"
	"testing"

	"github.com/cis/cis-backend/internal/models"
)

func ptr(v float64) *float64 { return &v }

func TestCombinedMetric(t *testing.T) {
	// US69's proposed default: normalise each input against the largest value in
	// the current set, then weight the two 50/50.
	counts := []int64{10, 5, 0}
	scores := []*float64{ptr(80), ptr(40), ptr(20)}

	got := combinedMetric(counts, scores)
	want := []float64{
		0.5*100 + 0.5*100, // 10/10 above-threshold, 80/80 average
		0.5*50 + 0.5*50,   // 5/10, 40/80
		0.5*0 + 0.5*25,    // 0/10, 20/80
	}

	for i := range want {
		if math.Abs(got[i]-want[i]) > 1e-9 {
			t.Errorf("box %d = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestCombinedMetricHandlesEmptyAndUnscoredSets(t *testing.T) {
	// A quiet week where nothing is above threshold must not divide by zero;
	// the average score alone then orders the topics.
	got := combinedMetric([]int64{0, 0}, []*float64{ptr(60), ptr(30)})
	if math.Abs(got[0]-50) > 1e-9 || math.Abs(got[1]-25) > 1e-9 {
		t.Errorf("all-zero counts = %v, want [50 25]", got)
	}

	// A topic whose claims are all unscored contributes nothing from the score
	// half rather than being dropped.
	got = combinedMetric([]int64{4, 2}, []*float64{nil, nil})
	if math.Abs(got[0]-50) > 1e-9 || math.Abs(got[1]-25) > 1e-9 {
		t.Errorf("unscored topics = %v, want [50 25]", got)
	}

	if len(combinedMetric(nil, nil)) != 0 {
		t.Error("empty set should produce no sizes")
	}
}

func TestThresholdStatus(t *testing.T) {
	cases := []struct {
		name      string
		score     *float64
		threshold float64
		want      string
	}{
		{"above", ptr(80), 70, models.ThresholdStatusOver},
		{"exactly at the threshold counts as over", ptr(70), 70, models.ThresholdStatusOver},
		{"below", ptr(69.9), 70, models.ThresholdStatusUnder},
		// Escalating on a missing score is the one direction that cannot be
		// defended to a reviewer.
		{"unscored", nil, 70, models.ThresholdStatusUnder},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := thresholdStatus(tc.score, tc.threshold); got != tc.want {
				t.Errorf("thresholdStatus = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPercent(t *testing.T) {
	if got := percent(3, 4); got != 75 {
		t.Errorf("percent(3,4) = %v, want 75", got)
	}
	if got := percent(0, 0); got != 0 {
		t.Errorf("percent on an empty repository = %v, want 0", got)
	}
}
