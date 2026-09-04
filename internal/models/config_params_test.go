package models

import (
	"math"
	"strconv"
	"testing"
)

// The registry is the single source for the seed, the write-path validation and
// the settings catalog. A mistake in it therefore shows up as a form that accepts
// a value the server rejects, or as a seeded default the validator would refuse —
// which is exactly what these tests rule out.

func TestEveryParamHasAValidDefault(t *testing.T) {
	for _, p := range ConfigParams {
		if p.Key == "" || p.Label == "" || p.Description == "" {
			t.Errorf("%q: key, label and description are all required", p.Key)
		}
		if p.Section == "" || p.Tier == "" {
			t.Errorf("%q: must belong to a tier and a section", p.Key)
		}
		if err := p.ValidateValue(p.Default); err != nil {
			t.Errorf("%q: its own default %q is invalid: %v", p.Key, p.Default, err)
		}
	}
}

func TestSectionsAreDeclared(t *testing.T) {
	declared := map[string]string{}
	for _, s := range ConfigSections {
		declared[s.Key] = s.Tier
	}

	for _, p := range ConfigParams {
		tier, ok := declared[p.Section]
		if !ok {
			t.Errorf("%q: section %q is not in ConfigSections, so the settings page would never render it",
				p.Key, p.Section)
			continue
		}
		if tier != p.Tier {
			t.Errorf("%q: is tier %q but its section %q is tier %q", p.Key, p.Tier, p.Section, tier)
		}
	}
}

func TestKeysAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range ConfigParams {
		if seen[p.Key] {
			t.Errorf("%q appears twice; the later row would silently shadow the earlier one", p.Key)
		}
		seen[p.Key] = true
	}
}

// TestDefaultsSatisfyTheCrossFieldRules is the one that matters on a fresh
// deployment: the seeded configuration must be a configuration the validator
// would accept, or the first save of any single weight fails on a rule the
// operator never broke.
func TestDefaultsSatisfyTheCrossFieldRules(t *testing.T) {
	defaults := map[string]string{}
	for _, p := range ConfigParams {
		defaults[p.Key] = p.Default
	}

	if errs := ValidateConfigSet(defaults); len(errs) > 0 {
		t.Errorf("the seeded defaults do not satisfy the cross-field rules: %v", errs)
	}
}

func TestSumGroupsAreEnforced(t *testing.T) {
	defaults := map[string]string{}
	for _, p := range ConfigParams {
		defaults[p.Key] = p.Default
	}

	// Moving one composite weight without compensating breaks the total, which
	// would otherwise silently lower every claim's score in the system.
	broken := map[string]string{}
	for k, v := range defaults {
		broken[k] = v
	}
	broken[SettingWeightReach] = "0.25"

	errs := ValidateConfigSet(broken)
	if _, ok := errs[SumGroupCompositeWeights]; !ok {
		t.Errorf("a composite weight sum of 1.10 was accepted: %v", errs)
	}
}

// TestPolicyDisruptionCeilingIsEnforced covers the bias guardrail: it is a hard
// cap rather than a recommendation precisely because the pressure to raise it
// arrives with a reason attached.
func TestPolicyDisruptionCeilingIsEnforced(t *testing.T) {
	param, ok := FindConfigParam(SettingHarmWeightPolicyDisruption)
	if !ok {
		t.Fatal("the Policy Disruption weight is missing from the registry")
	}
	if param.Max == nil || math.Abs(*param.Max-HarmPolicyDisruptionCeiling) > 1e-9 {
		t.Fatalf("ceiling = %v, want %v", param.Max, HarmPolicyDisruptionCeiling)
	}
	if err := param.ValidateValue("0.40"); err == nil {
		t.Error("0.40 was accepted above the 0.25 ceiling")
	}
}

func TestValidateValueRejectsWrongTypes(t *testing.T) {
	integer, _ := FindConfigParam(SettingCSIWindowDays)
	if err := integer.ValidateValue("7.5"); err == nil {
		t.Error("a fractional day count was accepted for an integer parameter")
	}
	if err := integer.ValidateValue(""); err == nil {
		t.Error("an empty value was accepted")
	}

	number, _ := FindConfigParam(SettingDiscountGamma)
	if err := number.ValidateValue("not-a-number"); err == nil {
		t.Error("a non-numeric gamma was accepted")
	}
	if err := number.ValidateValue("1.5"); err == nil {
		t.Error("a gamma above 1 was accepted, which would let pushback erase a score entirely")
	}
}

func TestDerivedAndManagedParamsAreNotWritable(t *testing.T) {
	for _, p := range ConfigParams {
		if p.Derived && p.Writable() {
			t.Errorf("%q is derived but writable; a stored copy could disagree with its source", p.Key)
		}
		if p.ManagedBy != "" && p.Writable() {
			t.Errorf("%q has a dedicated endpoint but is also writable here, skipping its validation", p.Key)
		}
	}

	writable := WritableConfigParams()
	for _, p := range writable {
		if !p.Writable() {
			t.Errorf("%q is in WritableConfigParams but reports itself unwritable", p.Key)
		}
	}
}

// TestBandOrderingIsEnforced guards the gauge: overlapping bands make a score
// fall in two colours at once, and which one wins becomes an implementation
// detail of the switch statement.
func TestBandOrderingIsEnforced(t *testing.T) {
	values := map[string]string{
		SettingCSIBandRiskyCeiling: "70",
		SettingCSIBandWatchCeiling: "60",
	}
	if _, ok := ValidateConfigSet(values)[SettingCSIBandWatchCeiling]; !ok {
		t.Error("an inverted band pair was accepted")
	}
}

func TestVelocityRangeMustBeARange(t *testing.T) {
	values := map[string]string{
		SettingVelocityZMin: "3",
		SettingVelocityZMax: "-3",
	}
	if _, ok := ValidateConfigSet(values)[SettingVelocityZMax]; !ok {
		t.Error("an inverted z-score range was accepted, which would invert every Velocity score")
	}
}

// TestPartialSetsAreNotFalselyRejected: UpdateConfigParams merges over the
// stored values before validating, but a caller holding only part of a group
// must not be told the group is broken on the strength of what it left out.
func TestPartialSetsAreNotFalselyRejected(t *testing.T) {
	partial := map[string]string{SettingWeightReach: "0.15"}
	if errs := ValidateConfigSet(partial); len(errs) > 0 {
		t.Errorf("an incomplete group was reported as invalid: %v", errs)
	}
}

// TestParamIDsAreUnique keeps each parameter's tracking code pointing at a
// single row. AP-12 is deliberately shared: the velocity z-score reference
// range is one conceptual value but needs two independent bounds (a floor and
// a ceiling) here.
func TestParamIDsAreUnique(t *testing.T) {
	shared := map[string]bool{"AP-12": true}

	seen := map[string]string{}
	for _, p := range ConfigParams {
		if p.ParamID == "" || shared[p.ParamID] {
			continue
		}
		if prior, ok := seen[p.ParamID]; ok {
			t.Errorf("%s is claimed by both %q and %q", p.ParamID, prior, p.Key)
		}
		seen[p.ParamID] = p.Key
	}
}

// TestValueTypeMapsOntoTheColumn: cis_settings.value_type has no "integer",
// so an integer parameter has to declare itself a number there or the stored
// row describes a type the column does not use.
func TestValueTypeMapsOntoTheColumn(t *testing.T) {
	for _, p := range ConfigParams {
		switch p.ValueType() {
		case ConfigTypeNumber, ConfigTypeString, ConfigTypeBoolean:
		default:
			t.Errorf("%q declares an unstorable value type %q", p.Key, p.ValueType())
		}

		if p.Type == ConfigTypeInteger {
			if _, err := strconv.Atoi(p.Default); err != nil {
				t.Errorf("%q is an integer parameter with a non-integer default %q", p.Key, p.Default)
			}
		}
	}
}
