package repository

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func ptr(v float64) *float64 { return &v }

func requireScore(t *testing.T, got *float64, want float64, label string) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s: got nil, want %v", label, want)
	}
	if diff := *got - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("%s: got %v, want %v", label, *got, want)
	}
}

// TestMergeBucketsAveragesAcrossBothSources is the point of reading two score
// history tables at once: a bucket holding rows from the backend's sampled
// snapshots and from the AI service's event-driven ones must average every
// underlying row equally, not average the two tables' averages.
func TestMergeBucketsAveragesAcrossBothSources(t *testing.T) {
	claim := uuid.New()
	bucket := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)

	points := mergeBuckets([]bucketAggregate{
		// cis_claim_score_snapshots: 3 rows averaging 60.
		{
			ClaimID: claim, BucketStart: bucket,
			FinalSum: ptr(180), FinalCount: 3,
			ClaimSum: ptr(240), ClaimCount: 3,
			SampleCount: 3,
		},
		// claim_score_snapshots: 1 row of 100, and no claim_score column at all.
		{
			ClaimID: claim, BucketStart: bucket,
			FinalSum: ptr(100), FinalCount: 1,
			ClaimSum: nil, ClaimCount: 0,
			SampleCount: 1,
		},
	})

	if len(points) != 1 {
		t.Fatalf("got %d points, want the two sources collapsed into 1", len(points))
	}
	// 280/4, not (60+100)/2 — the single AI row must not outweigh three backend
	// rows.
	requireScore(t, points[0].FinalClaimScore, 70, "final_claim_score")
	requireScore(t, points[0].ClaimScore, 80, "claim_score")
	if points[0].SampleCount != 4 {
		t.Errorf("sample_count: got %d, want 4", points[0].SampleCount)
	}
}

// TestMergeBucketsOrdersChronologically guards the chart's x-axis: points are
// merged through a map, so an explicit sort is the only thing keeping them in
// order.
func TestMergeBucketsOrdersChronologically(t *testing.T) {
	claim := uuid.New()
	jan := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	feb := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	mar := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	points := mergeBuckets([]bucketAggregate{
		{ClaimID: claim, BucketStart: mar, FinalSum: ptr(30), FinalCount: 1, SampleCount: 1},
		{ClaimID: claim, BucketStart: jan, FinalSum: ptr(10), FinalCount: 1, SampleCount: 1},
		{ClaimID: claim, BucketStart: feb, FinalSum: ptr(20), FinalCount: 1, SampleCount: 1},
	})

	want := []time.Time{jan, feb, mar}
	if len(points) != len(want) {
		t.Fatalf("got %d points, want %d", len(points), len(want))
	}
	for i, w := range want {
		if !points[i].BucketStart.Equal(w) {
			t.Errorf("point %d: got %s, want %s", i, points[i].BucketStart, w)
		}
	}
}

// TestMergeBucketsKeepsClaimsSeparate checks the F3 chart's multi-claim series
// are not collapsed into one line by the bucket key.
func TestMergeBucketsKeepsClaimsSeparate(t *testing.T) {
	first, second := uuid.New(), uuid.New()
	bucket := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)

	points := mergeBuckets([]bucketAggregate{
		{ClaimID: first, BucketStart: bucket, FinalSum: ptr(40), FinalCount: 1, SampleCount: 1},
		{ClaimID: second, BucketStart: bucket, FinalSum: ptr(80), FinalCount: 1, SampleCount: 1},
	})

	if len(points) != 2 {
		t.Fatalf("got %d points, want one per claim", len(points))
	}
	for _, p := range points {
		switch p.ClaimID {
		case first:
			requireScore(t, p.FinalClaimScore, 40, "first claim")
		case second:
			requireScore(t, p.FinalClaimScore, 80, "second claim")
		default:
			t.Errorf("unexpected claim id %s", p.ClaimID)
		}
	}
}

// TestMergeBucketsNullScore covers a claim the AI service has never scored: the
// column must stay null rather than collapsing to 0, which the API contract
// distinguishes ("a null score means not computed yet — not zero").
func TestMergeBucketsNullScore(t *testing.T) {
	claim := uuid.New()
	bucket := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)

	points := mergeBuckets([]bucketAggregate{
		{ClaimID: claim, BucketStart: bucket, FinalSum: nil, FinalCount: 0, SampleCount: 2},
	})

	if len(points) != 1 {
		t.Fatalf("got %d points, want 1", len(points))
	}
	if points[0].FinalClaimScore != nil {
		t.Errorf("final_claim_score: got %v, want nil", *points[0].FinalClaimScore)
	}
	if points[0].ClaimScore != nil {
		t.Errorf("claim_score: got %v, want nil", *points[0].ClaimScore)
	}
	if points[0].SampleCount != 2 {
		t.Errorf("sample_count: got %d, want 2", points[0].SampleCount)
	}
}
