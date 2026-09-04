package models

import (
	"testing"
	"time"
)

func TestNormalizeClaimType(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"existing", ClaimTypeExisting},
		{"generic", ClaimTypeExisting},
		{"GENERIC", ClaimTypeExisting},
		{"  Existing_Claim  ", ClaimTypeExisting},
		{"synthetic", ClaimTypeNonExisting},
		{"non_existing", ClaimTypeNonExisting},
		{"non-existing", ClaimTypeNonExisting},
		{"predicted", ClaimTypeNonExisting},
		// An unrecognized value must fall back to non-existing: presenting an
		// unknown claim as unscored is safer than implying it carries a score.
		{"something-new", ClaimTypeNonExisting},
		{"", ClaimTypeNonExisting},
	}

	for _, tc := range cases {
		if got := NormalizeClaimType(tc.raw); got != tc.want {
			t.Errorf("NormalizeClaimType(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestDeriveStatus(t *testing.T) {
	now := time.Date(2026, 8, 30, 14, 30, 0, 0, time.UTC)

	cases := []struct {
		name       string
		rolledOut  time.Time
		wantStatus string
	}{
		{"past date is rolled out", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), PolicyStatusRolledOut},
		// "On or before the current date" makes today inclusive.
		{"today is rolled out", time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC), PolicyStatusRolledOut},
		{"tomorrow is not rolled out", time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC), PolicyStatusNotRolledOut},
		{"future date is not rolled out", time.Date(2027, 3, 15, 0, 0, 0, 0, time.UTC), PolicyStatusNotRolledOut},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DeriveStatus(tc.rolledOut, now); got != tc.wantStatus {
				t.Errorf("DeriveStatus(%s) = %q, want %q", tc.rolledOut.Format("2006-01-02"), got, tc.wantStatus)
			}
		})
	}
}

func TestIsValidReviewStatus(t *testing.T) {
	// Prebunk and Debunk were merged into the single shared Action Taken
	// status, so exactly these four are valid.
	for _, s := range []string{"unreviewed", "active", "inactive", "action_taken"} {
		if !IsValidReviewStatus(s) {
			t.Errorf("expected %q to be a valid review status", s)
		}
	}
	for _, s := range []string{"debunk", "prebunk", "", "ACTIVE", "deleted"} {
		if IsValidReviewStatus(s) {
			t.Errorf("expected %q to be rejected as a review status", s)
		}
	}
}

// Both jsonb-backed types must hand the driver a string, never a []byte.
//
// Regression test for the 500 on PUT /networks/{id}/status: under
// PreferSimpleProtocol pgx interpolates a []byte argument as a bytea hex
// literal, which Postgres rejects for a jsonb column with "invalid input
// syntax for type json" (SQLSTATE 22P02).
func TestJSONBValueIsStringNotBytes(t *testing.T) {
	v, err := JSONB(`{"coordination_score":55.28}`).Value()
	if err != nil {
		t.Fatalf("JSONB.Value() error = %v", err)
	}
	s, ok := v.(string)
	if !ok {
		t.Fatalf("JSONB.Value() returned %T, want string", v)
	}
	if s != `{"coordination_score":55.28}` {
		t.Errorf("JSONB.Value() = %q, want the document verbatim", s)
	}

	// An empty document still stores as SQL NULL.
	if v, err := JSONB(nil).Value(); err != nil || v != nil {
		t.Errorf("JSONB(nil).Value() = (%v, %v), want (nil, nil)", v, err)
	}

	v, err = StringList{"sy", "du"}.Value()
	if err != nil {
		t.Fatalf("StringList.Value() error = %v", err)
	}
	s, ok = v.(string)
	if !ok {
		t.Fatalf("StringList.Value() returned %T, want string", v)
	}
	if s != `["sy","du"]` {
		t.Errorf("StringList.Value() = %q, want a JSON array", s)
	}

	if v, err := StringList(nil).Value(); err != nil || v != nil {
		t.Errorf("StringList(nil).Value() = (%v, %v), want (nil, nil)", v, err)
	}
}
