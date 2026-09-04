package models

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// The dynamic-parameter registry: every value that may be changed at runtime
// through Admin Settings instead of a redeploy.
//
// # Why a registry and not thirty constants
//
// Each parameter here has to be described in four places at once — the seed
// SQL, the write-path validation, the catalog the frontend renders its form
// from, and the two hand-off documents (FE_DYNAMIC_PARAMETER.md,
// AI_DYNAMIC_PARAMETER.md). Four hand-maintained copies of a bound is four
// chances for the form to accept a value the server then rejects, which is the
// exact failure DetectorParamRanges already exists to prevent for the
// detector's own settings. So the table below is the single source, and
// everything else is generated from it.
//
// # Where the values actually live
//
// In cis_settings — one row per key, `key` and `value` as text, with the type
// declared here rather than in the column. That store is shared: the backend
// writes it (the frontend's only write path is through this API) and the AI
// service reads the keys marked ConfigOwnerAI or ConfigOwnerShared. See
// docs/local_docs/AI_DYNAMIC_PARAMETER.md.
//
// # What is deliberately NOT here
//
//   - The detector's ~30 parameters. They live in cis_detector_settings as
//     typed columns because two of their constraints are cross-field, which a
//     flat key/value setter cannot check. See CISDetectorSettings.
//   - Infrastructure: ports, pool sizes, timeouts, cron expressions, storage
//     credentials. Changing those is a deployment act, not an operator act, and
//     they stay in the environment. See .env.example.

// Config tiers. The split is by WHO changes a parameter, because that is what
// decides which screen it belongs on — not by which formula consumes it.
const (
	// ConfigTierOperations is the city-facing tier: values a non-technical
	// administrator can reason about from the product's own vocabulary.
	ConfigTierOperations = "operations"
	// ConfigTierAnalytics is the model tier: values that change what a score
	// means. An admin may hold the pen, but the decision belongs with the
	// engineering/data team.
	ConfigTierAnalytics = "analytics"
)

// Which service reads a parameter. The write path is always the same — the
// frontend, through this backend — so only the read side varies.
const (
	ConfigOwnerBackend = "backend"
	ConfigOwnerAI      = "ai"
	ConfigOwnerShared  = "shared"
)

// Declared value types, mirroring CISSetting.ValueType.
const (
	ConfigTypeNumber  = "number"
	ConfigTypeInteger = "integer"
	ConfigTypeString  = "string"
	ConfigTypeBoolean = "boolean"
)

// Sum groups: sets whose members must add up to exactly 1.00. Named here so
// the validator, the catalog and the settings form's running total all mean
// the same thing by "the composite weights".
const (
	SumGroupCompositeWeights = "composite_weights"
	SumGroupHarmWeights      = "harm_weights"
	SumGroupCSIWeights       = "csi_weights"
	SumGroupTreemapWeights   = "treemap_weights"
)

// Setting keys for the dynamic parameters.
//
// The three that predate this registry — SettingAlertThreshold,
// SettingMonitoredCity, SettingCityTimezone — keep their original names. A key
// is the identity of a stored row, so renaming one would orphan an operator's
// saved value behind a fresh default.
const (
	// Composite Claim Score weights.
	SettingWeightReach              = "scoring.weight_reach"
	SettingWeightVelocity           = "scoring.weight_velocity"
	SettingWeightFalseness          = "scoring.weight_falseness"
	SettingWeightHarm               = "scoring.weight_harm"
	SettingWeightEmotionalIntensity = "scoring.weight_emotional_intensity"

	// Harm Severity sub-weights.
	SettingHarmWeightPublicSafety       = "scoring.harm_weight_public_safety"
	SettingHarmWeightInstitutionalTrust = "scoring.harm_weight_institutional_trust"
	SettingHarmWeightEconomic           = "scoring.harm_weight_economic"
	SettingHarmWeightPolicyDisruption   = "scoring.harm_weight_policy_disruption"

	// Reach & Velocity normalisation.
	SettingReachWindowDays      = "scoring.reach_normalization_window_days"
	SettingReachWeightImpress   = "scoring.reach_weight_impressions"
	SettingReachWeightAuthors   = "scoring.reach_weight_unique_authors"
	SettingReachWeightContent   = "scoring.reach_weight_content_count"
	SettingReachWeightPlatforms = "scoring.reach_weight_platform_spread"
	SettingVelocityIntervalHrs  = "scoring.velocity_interval_hours"
	SettingVelocityZMin         = "scoring.velocity_zscore_min"
	SettingVelocityZMax         = "scoring.velocity_zscore_max"
	SettingVelocityEpsilon      = "scoring.velocity_epsilon"

	// Net Pushback Ratio and its discount.
	SettingNPRWindowHours        = "scoring.npr_window_hours"
	SettingDiscountGamma         = "scoring.discount_gamma"
	SettingNPRReliabilityMinimum = "scoring.npr_reliability_minimum_posts"

	// Falseness Confidence.
	SettingFalsenessMatchThreshold = "scoring.falseness_match_threshold"
	SettingFalsenessLiveMatchScore = "scoring.falseness_live_match_score"

	// Clustering and matchmaking similarity gates.
	SettingClaimAttachThreshold  = "clustering.claim_attach_threshold"
	SettingTopicAttachThreshold  = "clustering.topic_attach_threshold"
	SettingPolicyMatchPrefilter  = "matchmaking.claim_prefilter_threshold"
	SettingDebunkSegmentMaxCount = "ai.debunk_segment_max_count"

	// Indonesia Climate Sentiment Index.
	SettingCSIWeightBCS        = "csi.weight_bcs"
	SettingCSIWeightRiskLoad   = "csi.weight_risk_load"
	SettingCSIWindowDays       = "csi.window_days"
	SettingCSIMomentumLagHours = "csi.momentum_lag_hours"
	SettingCSIMinimumVolume    = "csi.minimum_volume"
	SettingCSIBandRiskyCeiling = "csi.band_risky_ceiling"
	SettingCSIBandWatchCeiling = "csi.band_watch_ceiling"

	// Overview presentation.
	SettingTreemapWeightAboveCount = "overview.treemap_weight_above_count"
	SettingTreemapWeightAvgScore   = "overview.treemap_weight_avg_score"
	SettingTopPolicyLimit          = "overview.top_policy_limit"
	SettingMoMWindowDays           = "overview.mom_window_days"

	// Public Policy Bank.
	SettingPolicyUploadWarnMB = "policy.upload_warn_size_mb"

	// Score history retention.
	SettingScoreSnapshotRetentionDays = "alerts.score_snapshot_retention_days"
)

// HarmPolicyDisruptionCeiling is a hard cap, not a recommendation.
//
// PolicyDisruption is weighted lowest of the harm sub-scores because scoring
// "criticism of the government's own policy" as harm carries inherent bias
// risk; a ceiling is what stops that weight being tuned upward until the tool
// starts ranking critics. It is named here because it is a rule, not a
// starting value — every other default lives once, as the Default field of
// its registry row.
const HarmPolicyDisruptionCeiling = 0.25

// ConfigParam describes one dynamically-configurable value.
type ConfigParam struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	// Tier is ConfigTierOperations or ConfigTierAnalytics — who is expected to
	// change this, which is what decides the screen it belongs on.
	Tier string `json:"tier"`
	// Section groups semantically related parameters within a tier, so the
	// settings form can render one fieldset per section without inventing its
	// own grouping.
	Section string `json:"section"`
	Type    string `json:"type"`
	Default string `json:"default"`
	// Min and Max bound a numeric parameter. Nil on both means unbounded, which
	// only ever applies to non-numeric types.
	Min  *float64 `json:"min,omitempty"`
	Max  *float64 `json:"max,omitempty"`
	Unit string   `json:"unit,omitempty"`
	// Owner names the service that READS the value. The write path is always
	// the frontend through this backend.
	Owner string `json:"owner"`
	// SumGroup, when set, names a set whose members must total exactly 1.00.
	SumGroup string `json:"sum_group,omitempty"`
	// Derived marks a value that is computed from another parameter and has no
	// stored row: it is served read-only so the settings form can show it
	// beside its source.
	Derived bool `json:"derived,omitempty"`
	// ManagedBy names a dedicated endpoint when a parameter needs validation
	// the generic setter cannot perform (a city catalog, an IANA zone table).
	// Such a parameter is read through the catalog and written only there.
	ManagedBy string `json:"managed_by,omitempty"`
	PRDRef    string `json:"prd_ref,omitempty"`
	// ParamID is a short tracking code for this parameter, carried through so
	// a row here can be cross-referenced elsewhere.
	ParamID     string `json:"param_id,omitempty"`
	Description string `json:"description"`
	// Note carries a caveat the bounds alone do not express.
	Note string `json:"note,omitempty"`
}

// ConfigSection labels one fieldset of the settings form.
type ConfigSection struct {
	Key         string `json:"key"`
	Tier        string `json:"tier"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

// Section keys, in the order the settings form should render them.
const (
	SectionAlerting          = "alerting"
	SectionOverviewDisplay   = "overview_display"
	SectionPolicyBank        = "policy_bank"
	SectionContentGeneration = "content_generation"
	SectionScope             = "scope"

	SectionCompositeWeights = "composite_weights"
	SectionHarmWeights      = "harm_weights"
	SectionReachVelocity    = "reach_velocity"
	SectionPushback         = "pushback"
	SectionFalseness        = "falseness"
	SectionClustering       = "clustering"
	SectionSentimentIndex   = "sentiment_index"
	SectionOverviewRanking  = "overview_ranking"
	SectionRetention        = "retention"
)

// ConfigSections describes each fieldset, in display order.
var ConfigSections = []ConfigSection{
	{Key: SectionAlerting, Tier: ConfigTierOperations, Title: "Alerting & risk threshold",
		Description: "What counts as an elevated claim, everywhere in the product."},
	{Key: SectionOverviewDisplay, Tier: ConfigTierOperations, Title: "Overview page display",
		Description: "How much the Overview page shows and how far back it compares."},
	{Key: SectionPolicyBank, Tier: ConfigTierOperations, Title: "Public Policy Bank",
		Description: "Upload guidance for policy documents."},
	{Key: SectionContentGeneration, Tier: ConfigTierOperations, Title: "AI content generation",
		Description: "How much response copy the AI drafts per claim."},
	{Key: SectionScope, Tier: ConfigTierOperations, Title: "City & locale",
		Description: "The single city this instance monitors, and the timezone its reports are stamped in."},

	{Key: SectionCompositeWeights, Tier: ConfigTierAnalytics, Title: "Claim score — composite weights",
		Description: "How the five parameters combine into a claim's score. Must total 1.00."},
	{Key: SectionHarmWeights, Tier: ConfigTierAnalytics, Title: "Harm severity — sub-weights",
		Description: "How the four harm dimensions combine into H. Must total 1.00."},
	{Key: SectionReachVelocity, Tier: ConfigTierAnalytics, Title: "Reach & velocity normalisation",
		Description: "How raw spread and growth are mapped onto the 0-100 scale."},
	{Key: SectionPushback, Tier: ConfigTierAnalytics, Title: "Public pushback discount",
		Description: "How much organic correction reduces a claim's score."},
	{Key: SectionFalseness, Tier: ConfigTierAnalytics, Title: "Falseness matching",
		Description: "How confidently a claim has to match a verified debunk to be scored false."},
	{Key: SectionClustering, Tier: ConfigTierAnalytics, Title: "Clustering & policy matchmaking",
		Description: "Similarity gates deciding what joins an existing claim, topic or policy."},
	{Key: SectionSentimentIndex, Tier: ConfigTierAnalytics, Title: "Climate Sentiment Index",
		Description: "The composition, window and health bands of the CSI gauge."},
	{Key: SectionOverviewRanking, Tier: ConfigTierAnalytics, Title: "Overview ranking formula",
		Description: "How topics and policies are sized and ranked against each other."},
	{Key: SectionRetention, Tier: ConfigTierAnalytics, Title: "History retention",
		Description: "How long score history is kept before it is pruned."},
}

func f(v float64) *float64 { return &v }

// ConfigParams is the complete registry, in display order within each tier.
var ConfigParams = []ConfigParam{
	// ---------------------------------------------------------------------
	// Tier 1 — Operations
	// ---------------------------------------------------------------------
	{
		Key: SettingAlertThreshold, Label: "Alert threshold",
		Tier: ConfigTierOperations, Section: SectionAlerting,
		Type: ConfigTypeNumber, Default: "70", Min: f(0), Max: f(100), Unit: "score",
		Owner: ConfigOwnerShared, PRDRef: "§8; US32, US29, US71", ParamID: "AP-16",
		Description: "The FinalClaimScore at or above which a claim reads as Over Threshold on the Alert page, " +
			"counts as above-threshold on the Overview, and can raise a threshold-crossing notification.",
		Note: "The most operationally active value in the system. The CSI risk threshold inherits from it.",
	},
	{
		Key: "csi.risk_threshold", Label: "CSI risk threshold (derived)",
		Tier: ConfigTierOperations, Section: SectionAlerting,
		Type: ConfigTypeNumber, Default: "70", Min: f(0), Max: f(100), Unit: "score",
		Owner: ConfigOwnerBackend, Derived: true, PRDRef: "§6.6.2; US67", ParamID: "AP-20",
		Description: "Minimum FinalClaimScore for a claim to count toward the Climate Sentiment Index's RiskLoad.",
		Note: "Not independently editable: it always equals the alert threshold, so \"elevated risk\" means " +
			"the same thing on the Alert page and on the Overview gauge. Displayed read-only beside it.",
	},
	{
		Key: SettingTopPolicyLimit, Label: "Top policies shown",
		Tier: ConfigTierOperations, Section: SectionOverviewDisplay,
		Type: ConfigTypeInteger, Default: "5", Min: f(1), Max: f(20), Unit: "policies",
		Owner: ConfigOwnerBackend, PRDRef: "§11; US70",
		Description: "How many policies the Overview's O3 leaderboard lists.",
		Note: "PRD US70's section heading says \"Top 10\" and its detail says top 5. This setting is how " +
			"that open question is answered without a redeploy.",
	},
	{
		Key: SettingMoMWindowDays, Label: "Month-on-month comparison window",
		Tier: ConfigTierOperations, Section: SectionOverviewDisplay,
		Type: ConfigTypeInteger, Default: "30", Min: f(7), Max: f(90), Unit: "days",
		Owner: ConfigOwnerBackend, PRDRef: "§11; US69",
		Description: "The window the O2 topic modal compares against the preceding one for its ▲/▼ change figure.",
	},
	{
		Key: SettingPolicyUploadWarnMB, Label: "Large upload warning",
		Tier: ConfigTierOperations, Section: SectionPolicyBank,
		Type: ConfigTypeInteger, Default: "50", Min: f(1), Max: f(2048), Unit: "MB",
		Owner: ConfigOwnerBackend, PRDRef: "§7; US40", ParamID: "AP-17",
		Description: "File size above which the Add Public Policy modal warns the uploader.",
		Note: "A soft warning, never a block. US40 requires policy uploads to have no size limit; this " +
			"flags an unusually large file rather than rejecting it.",
	},
	{
		Key: SettingDebunkSegmentMaxCount, Label: "Debunk segments per claim",
		Tier: ConfigTierOperations, Section: SectionContentGeneration,
		Type: ConfigTypeInteger, Default: "3", Min: f(1), Max: f(5), Unit: "variants",
		Owner: ConfigOwnerAI, PRDRef: "US12; US33", ParamID: "AP-21",
		Description: "How many audience-segmented debunk drafts the AI generates for each claim.",
		Note:        "Without a cap, a highly cross-cutting claim generates more drafts than anyone will review.",
	},
	{
		Key: SettingMonitoredCity, Label: "Monitored city",
		Tier: ConfigTierOperations, Section: SectionScope,
		Type: ConfigTypeString, Default: DefaultMonitoredCity,
		Owner: ConfigOwnerShared, ManagedBy: "PUT /api/v1/settings/city",
		PRDRef: "US65; §6.6.4", ParamID: "AP-22",
		Description: "The single Indonesian city this instance monitors. Scopes every metric on the Overview page.",
		Note: "Written through its own endpoint because the value must be one of a fixed catalog " +
			"(GET /api/v1/settings/cities), and selecting a city also moves the report timezone with it.",
	},
	{
		Key: SettingCityTimezone, Label: "City timezone",
		Tier: ConfigTierOperations, Section: SectionScope,
		Type: ConfigTypeString, Default: DefaultCityTimezone,
		Owner: ConfigOwnerBackend, ManagedBy: "PUT /api/v1/settings/city-timezone",
		PRDRef:      "§10.8",
		Description: "IANA zone for the city-local half of every F5 report footer timestamp.",
		Note:        "Follows the monitored city automatically; set it directly only to override.",
	},

	// ---------------------------------------------------------------------
	// Tier 2 — Analytics: composite weights
	// ---------------------------------------------------------------------
	{
		Key: SettingWeightReach, Label: "Weight — Reach (R)",
		Tier: ConfigTierAnalytics, Section: SectionCompositeWeights,
		Type: ConfigTypeNumber, Default: "0.15", Min: f(0), Max: f(1),
		Owner: ConfigOwnerShared, SumGroup: SumGroupCompositeWeights,
		PRDRef: "§6.3; US22", ParamID: "AP-01",
		Description: "Share of the composite score contributed by how far the claim has travelled.",
	},
	{
		Key: SettingWeightVelocity, Label: "Weight — Velocity (V)",
		Tier: ConfigTierAnalytics, Section: SectionCompositeWeights,
		Type: ConfigTypeNumber, Default: "0.15", Min: f(0), Max: f(1),
		Owner: ConfigOwnerShared, SumGroup: SumGroupCompositeWeights,
		PRDRef: "§6.3; US22", ParamID: "AP-02",
		Description: "Share contributed by how fast the claim is currently growing.",
	},
	{
		Key: SettingWeightFalseness, Label: "Weight — Falseness (F)",
		Tier: ConfigTierAnalytics, Section: SectionCompositeWeights,
		Type: ConfigTypeNumber, Default: "0.30", Min: f(0), Max: f(1),
		Owner: ConfigOwnerShared, SumGroup: SumGroupCompositeWeights,
		PRDRef: "§6.3; US22", ParamID: "AP-03",
		Description: "Share contributed by how confidently the claim is confirmed false.",
	},
	{
		Key: SettingWeightHarm, Label: "Weight — Harm (H)",
		Tier: ConfigTierAnalytics, Section: SectionCompositeWeights,
		Type: ConfigTypeNumber, Default: "0.30", Min: f(0), Max: f(1),
		Owner: ConfigOwnerShared, SumGroup: SumGroupCompositeWeights,
		PRDRef: "§6.3; US22", ParamID: "AP-04",
		Description: "Share contributed by the estimated real-world damage the claim could cause.",
	},
	{
		Key: SettingWeightEmotionalIntensity, Label: "Weight — Emotional Intensity (EI)",
		Tier: ConfigTierAnalytics, Section: SectionCompositeWeights,
		Type: ConfigTypeNumber, Default: "0.10", Min: f(0), Max: f(1),
		Owner: ConfigOwnerShared, SumGroup: SumGroupCompositeWeights,
		PRDRef: "§6.3; US22", ParamID: "AP-05",
		Description: "Share contributed by how angry the public reaction to the claim is.",
	},

	// --- Harm sub-weights ---
	{
		Key: SettingHarmWeightPublicSafety, Label: "Harm — Public Safety",
		Tier: ConfigTierAnalytics, Section: SectionHarmWeights,
		Type: ConfigTypeNumber, Default: "0.35", Min: f(0), Max: f(1),
		Owner: ConfigOwnerShared, SumGroup: SumGroupHarmWeights,
		PRDRef: "§6.2.4; US23", ParamID: "AP-06",
		Description: "Share of Harm Severity carried by risk to physical safety.",
	},
	{
		Key: SettingHarmWeightInstitutionalTrust, Label: "Harm — Institutional Trust",
		Tier: ConfigTierAnalytics, Section: SectionHarmWeights,
		Type: ConfigTypeNumber, Default: "0.30", Min: f(0), Max: f(1),
		Owner: ConfigOwnerShared, SumGroup: SumGroupHarmWeights,
		PRDRef: "§6.2.4; US23", ParamID: "AP-07",
		Description: "Share carried by erosion of trust in public institutions.",
	},
	{
		Key: SettingHarmWeightEconomic, Label: "Harm — Economic",
		Tier: ConfigTierAnalytics, Section: SectionHarmWeights,
		Type: ConfigTypeNumber, Default: "0.20", Min: f(0), Max: f(1),
		Owner: ConfigOwnerShared, SumGroup: SumGroupHarmWeights,
		PRDRef: "§6.2.4; US23", ParamID: "AP-08",
		Description: "Share carried by economic damage.",
	},
	{
		Key: SettingHarmWeightPolicyDisruption, Label: "Harm — Policy Disruption",
		Tier: ConfigTierAnalytics, Section: SectionHarmWeights,
		Type: ConfigTypeNumber, Default: "0.15", Min: f(0), Max: f(HarmPolicyDisruptionCeiling),
		Owner: ConfigOwnerShared, SumGroup: SumGroupHarmWeights,
		PRDRef: "§6.2.4; US23", ParamID: "AP-09",
		Description: "Share carried by concrete interference with policy execution.",
		Note: "Hard ceiling of 0.25, enforced on save rather than merely recommended. This is PRD 6.2.4's " +
			"bias guardrail: it stops the system being tuned until criticism of a government's own policy " +
			"scores as harm.",
	},

	// --- Reach & Velocity normalisation ---
	{
		Key: SettingReachWindowDays, Label: "Reach normalisation window",
		Tier: ConfigTierAnalytics, Section: SectionReachVelocity,
		Type: ConfigTypeInteger, Default: "90", Min: f(7), Max: f(365), Unit: "days",
		Owner: ConfigOwnerAI, PRDRef: "§6.2.1", ParamID: "AP-10",
		Description: "How far back R_min/R_max are observed before raw reach is normalised onto 0-100.",
		Note:        "Shorter reacts faster to a shifting baseline; longer smooths seasonal noise.",
	},
	{
		Key: SettingReachWeightImpress, Label: "Reach component — impressions (w1)",
		Tier: ConfigTierAnalytics, Section: SectionReachVelocity,
		Type: ConfigTypeNumber, Default: "0.25", Min: f(0), Max: f(1),
		Owner: ConfigOwnerAI, PRDRef: "§6.2.1",
		Description: "Weight of log(1+Impressions) inside raw reach.",
	},
	{
		Key: SettingReachWeightAuthors, Label: "Reach component — unique authors (w2)",
		Tier: ConfigTierAnalytics, Section: SectionReachVelocity,
		Type: ConfigTypeNumber, Default: "0.25", Min: f(0), Max: f(1),
		Owner: ConfigOwnerAI, PRDRef: "§6.2.1",
		Description: "Weight of log(1+UniqueAuthors) inside raw reach.",
	},
	{
		Key: SettingReachWeightContent, Label: "Reach component — content count (w3)",
		Tier: ConfigTierAnalytics, Section: SectionReachVelocity,
		Type: ConfigTypeNumber, Default: "0.25", Min: f(0), Max: f(1),
		Owner: ConfigOwnerAI, PRDRef: "§6.2.1",
		Description: "Weight of log(1+ContentCount) inside raw reach.",
	},
	{
		Key: SettingReachWeightPlatforms, Label: "Reach component — platform spread (w4)",
		Tier: ConfigTierAnalytics, Section: SectionReachVelocity,
		Type: ConfigTypeNumber, Default: "0.25", Min: f(0), Max: f(1),
		Owner: ConfigOwnerAI, PRDRef: "§6.2.1",
		Description: "Weight of the DistinctPlatforms/TotalMonitoredPlatforms ratio inside raw reach.",
	},
	{
		Key: SettingVelocityIntervalHrs, Label: "Velocity interval (Δ)",
		Tier: ConfigTierAnalytics, Section: SectionReachVelocity,
		Type: ConfigTypeInteger, Default: "6", Min: f(1), Max: f(72), Unit: "hours",
		Owner: ConfigOwnerAI, PRDRef: "§6.2.2", ParamID: "AP-11",
		Description: "The gap between the two volume readings whose difference is the growth rate.",
	},
	{
		Key: SettingVelocityZMin, Label: "Velocity z-score floor (Z_min)",
		Tier: ConfigTierAnalytics, Section: SectionReachVelocity,
		Type: ConfigTypeNumber, Default: "-3", Min: f(-10), Max: f(0), Unit: "σ",
		Owner: ConfigOwnerAI, PRDRef: "§6.2.2", ParamID: "AP-12",
		Description: "Standard deviations below baseline that map to a Velocity of 0.",
	},
	{
		Key: SettingVelocityZMax, Label: "Velocity z-score ceiling (Z_max)",
		Tier: ConfigTierAnalytics, Section: SectionReachVelocity,
		Type: ConfigTypeNumber, Default: "3", Min: f(0), Max: f(10), Unit: "σ",
		Owner: ConfigOwnerAI, PRDRef: "§6.2.2", ParamID: "AP-12",
		Description: "Standard deviations above baseline that map to a Velocity of 100.",
		Note:        "Must be greater than the floor.",
	},
	{
		Key: SettingVelocityEpsilon, Label: "Velocity epsilon (ε)",
		Tier: ConfigTierAnalytics, Section: SectionReachVelocity,
		Type: ConfigTypeNumber, Default: "0.0001", Min: f(0.000001), Max: f(1),
		Owner: ConfigOwnerAI, PRDRef: "§6.2.2", ParamID: "AP-13",
		Description: "Division-by-zero guard for a brand-new claim with no prior volume.",
	},

	// --- Pushback discount ---
	{
		Key: SettingNPRWindowHours, Label: "Pushback rolling window",
		Tier: ConfigTierAnalytics, Section: SectionPushback,
		Type: ConfigTypeInteger, Default: "36", Min: f(1), Max: f(168), Unit: "hours",
		Owner: ConfigOwnerAI, PRDRef: "§6.4.3", ParamID: "AP-14",
		Description: "The window over which supporting and opposing volume are compared to compute NPR.",
		Note:        "PRD 6.4.3 recommends 24-48 hours.",
	},
	{
		Key: SettingDiscountGamma, Label: "Discount dampening cap (γ)",
		Tier: ConfigTierAnalytics, Section: SectionPushback,
		Type: ConfigTypeNumber, Default: "0.5", Min: f(0), Max: f(1),
		Owner: ConfigOwnerShared, PRDRef: "§6.4.4", ParamID: "AP-15",
		Description: "The largest share of a claim's score that organic pushback can remove.",
		Note: "At the default 0.5, even total pushback halves a score rather than erasing it — PRD 6.4.4's " +
			"design intent that a contested claim is de-prioritised, never hidden.",
	},
	{
		Key: SettingNPRReliabilityMinimum, Label: "Pushback reliability floor",
		Tier: ConfigTierAnalytics, Section: SectionPushback,
		Type: ConfigTypeInteger, Default: "25", Min: f(0), Max: f(1000), Unit: "posts",
		Owner: ConfigOwnerAI, PRDRef: "§6.4.7",
		Description: "Total volume below which no discount is applied, because the pushback signal is too thin to trust.",
	},

	// --- Falseness ---
	{
		Key: SettingFalsenessMatchThreshold, Label: "Debunk match threshold",
		Tier: ConfigTierAnalytics, Section: SectionFalseness,
		Type: ConfigTypeNumber, Default: "0.55", Min: f(0), Max: f(1), Unit: "cosine",
		Owner: ConfigOwnerAI, PRDRef: "§6.2.3",
		Description: "Minimum similarity to a verified official source before a claim is scored as false.",
		Note:        "Below it F is left unset rather than scored 0 — 0 would wrongly assert \"confirmed true\".",
	},
	{
		Key: SettingFalsenessLiveMatchScore, Label: "Live fact-check match score",
		Tier: ConfigTierAnalytics, Section: SectionFalseness,
		Type: ConfigTypeNumber, Default: "75", Min: f(0), Max: f(100), Unit: "score",
		Owner: ConfigOwnerAI, PRDRef: "§6.2.3",
		Description: "Falseness score assigned when the live Fact Check API returns a false verdict.",
		Note: "A fixed score rather than a modelled similarity, because a published verdict is a real " +
			"judgement rather than a distance between two embeddings.",
	},

	// --- Clustering & matchmaking ---
	{
		Key: SettingClaimAttachThreshold, Label: "Claim attach threshold",
		Tier: ConfigTierAnalytics, Section: SectionClustering,
		Type: ConfigTypeNumber, Default: "0.55", Min: f(0), Max: f(1), Unit: "cosine",
		Owner: ConfigOwnerAI, PRDRef: "§6.2.1",
		Description: "Similarity at which a new post joins an existing claim rather than seeding a new one.",
	},
	{
		Key: SettingTopicAttachThreshold, Label: "Topic attach threshold",
		Tier: ConfigTierAnalytics, Section: SectionClustering,
		Type: ConfigTypeNumber, Default: "0.5", Min: f(0), Max: f(1), Unit: "cosine",
		Owner: ConfigOwnerAI, PRDRef: "US42",
		Description: "Similarity at which a new claim joins an existing topic rather than creating one.",
	},
	{
		Key: SettingPolicyMatchPrefilter, Label: "Policy matchmaking prefilter",
		Tier: ConfigTierAnalytics, Section: SectionClustering,
		Type: ConfigTypeNumber, Default: "0.35", Min: f(0), Max: f(1), Unit: "cosine",
		Owner: ConfigOwnerAI, PRDRef: "US42",
		Description: "Similarity a claim must reach to be sent to the LLM as a candidate match for a policy.",
		Note:        "A recall knob: lower widens the candidate set and costs more LLM calls per upload.",
	},

	// --- Climate Sentiment Index ---
	{
		Key: SettingCSIWeightBCS, Label: "CSI weight — baseline sentiment",
		Tier: ConfigTierAnalytics, Section: SectionSentimentIndex,
		Type: ConfigTypeNumber, Default: "0.5", Min: f(0), Max: f(1),
		Owner: ConfigOwnerBackend, SumGroup: SumGroupCSIWeights,
		PRDRef: "§6.6; US67, US68", ParamID: "AP-18",
		Description: "Share of the index carried by the overall tone of the climate conversation.",
	},
	{
		Key: SettingCSIWeightRiskLoad, Label: "CSI weight — risk load (inverted)",
		Tier: ConfigTierAnalytics, Section: SectionSentimentIndex,
		Type: ConfigTypeNumber, Default: "0.5", Min: f(0), Max: f(1),
		Owner: ConfigOwnerBackend, SumGroup: SumGroupCSIWeights,
		PRDRef: "§6.6; US67, US68", ParamID: "AP-19",
		Description: "Share carried by the burden of serious claims on the conversation.",
		Note: "Weighted equally with baseline sentiment by default so a calm-sounding but dangerous " +
			"conversation cannot score as healthy on tone alone.",
	},
	{
		Key: SettingCSIWindowDays, Label: "CSI rolling window",
		Tier: ConfigTierAnalytics, Section: SectionSentimentIndex,
		Type: ConfigTypeInteger, Default: "7", Min: f(1), Max: f(90), Unit: "days",
		Owner: ConfigOwnerBackend, PRDRef: "§6.6.3",
		Description: "The rolling average behind the headline gauge figure.",
		Note:        "PRD 6.6.3 fixes this at 7 days to stop a single viral event swinging the index.",
	},
	{
		Key: SettingCSIMomentumLagHours, Label: "CSI momentum lag",
		Tier: ConfigTierAnalytics, Section: SectionSentimentIndex,
		Type: ConfigTypeInteger, Default: "24", Min: f(1), Max: f(168), Unit: "hours",
		Owner: ConfigOwnerBackend, PRDRef: "§6.6.3",
		Description: "How far behind the headline window the comparison window sits, giving the direction arrow.",
		Note:        "PRD 6.6.3 recommends 24-48 hours.",
	},
	{
		Key: SettingCSIMinimumVolume, Label: "CSI minimum activity",
		Tier: ConfigTierAnalytics, Section: SectionSentimentIndex,
		Type: ConfigTypeInteger, Default: "100", Min: f(1), Max: f(1000000), Unit: "items",
		Owner: ConfigOwnerBackend, PRDRef: "§6.6.3",
		Description: "Conversation volume below which the gauge reads \"Insufficient Data\" instead of a score.",
		Note:        "Without a floor, a quiet week reports a falsely calm environment.",
	},
	{
		Key: SettingCSIBandRiskyCeiling, Label: "CSI band — risky ceiling",
		Tier: ConfigTierAnalytics, Section: SectionSentimentIndex,
		Type: ConfigTypeNumber, Default: "33.33", Min: f(0), Max: f(100), Unit: "score",
		Owner: ConfigOwnerBackend, PRDRef: "§6.6.5; US68",
		Description: "Index value below which the gauge shows red.",
	},
	{
		Key: SettingCSIBandWatchCeiling, Label: "CSI band — watch ceiling",
		Tier: ConfigTierAnalytics, Section: SectionSentimentIndex,
		Type: ConfigTypeNumber, Default: "66.67", Min: f(0), Max: f(100), Unit: "score",
		Owner: ConfigOwnerBackend, PRDRef: "§6.6.5; US68",
		Description: "Index value below which the gauge shows amber, and at or above which it shows green.",
		Note: "Must be greater than the risky ceiling. The PRD names the three colours but gives no cut " +
			"points; the defaults split the scale into equal thirds.",
	},

	// --- Overview ranking ---
	{
		Key: SettingTreemapWeightAboveCount, Label: "Ranking weight — claims above threshold",
		Tier: ConfigTierAnalytics, Section: SectionOverviewRanking,
		Type: ConfigTypeNumber, Default: "0.5", Min: f(0), Max: f(1),
		Owner: ConfigOwnerBackend, SumGroup: SumGroupTreemapWeights, PRDRef: "§11; US69, US70",
		Description: "Share of a topic's treemap box size, and a policy's leaderboard rank, driven by how " +
			"many of its claims are above threshold.",
	},
	{
		Key: SettingTreemapWeightAvgScore, Label: "Ranking weight — average claim score",
		Tier: ConfigTierAnalytics, Section: SectionOverviewRanking,
		Type: ConfigTypeNumber, Default: "0.5", Min: f(0), Max: f(1),
		Owner: ConfigOwnerBackend, SumGroup: SumGroupTreemapWeights, PRDRef: "§11; US69, US70",
		Description: "Share driven by the average score of its claims.",
		Note: "US69 leaves this formula open and proposes an equal split; these two settings are that " +
			"open question made adjustable.",
	},

	// --- Retention ---
	{
		Key: SettingScoreSnapshotRetentionDays, Label: "Score history retention",
		Tier: ConfigTierAnalytics, Section: SectionRetention,
		Type: ConfigTypeInteger, Default: "400", Min: f(30), Max: f(3650), Unit: "days",
		Owner: ConfigOwnerBackend, PRDRef: "US27",
		Description: "How long per-claim score snapshots are kept before the hourly job prunes them.",
		Note: "The Alert page chart can only plot as far back as this. The default clears a full year " +
			"plus a margin, so a Year granularity view is never short of data.",
	},
}

// configParamIndex is built once at init so lookups are not linear scans over
// the registry on every write.
var configParamIndex = func() map[string]ConfigParam {
	index := make(map[string]ConfigParam, len(ConfigParams))
	for _, p := range ConfigParams {
		index[p.Key] = p
	}
	return index
}()

// FindConfigParam returns the registry entry for a key.
func FindConfigParam(key string) (ConfigParam, bool) {
	p, ok := configParamIndex[key]
	return p, ok
}

// WritableConfigParams returns the parameters the generic setter accepts:
// everything that is neither derived nor owned by a dedicated endpoint.
func WritableConfigParams() []ConfigParam {
	out := make([]ConfigParam, 0, len(ConfigParams))
	for _, p := range ConfigParams {
		if p.Writable() {
			out = append(out, p)
		}
	}
	return out
}

// Writable reports whether PUT /settings/parameters may set this key.
func (p ConfigParam) Writable() bool { return !p.Derived && p.ManagedBy == "" }

// ValueType maps the registry's declared type onto CISSetting.ValueType, which
// stores integers as plain numbers.
func (p ConfigParam) ValueType() string {
	if p.Type == ConfigTypeInteger {
		return ConfigTypeNumber
	}
	return p.Type
}

// DefaultFloat parses the declared default of a numeric parameter.
//
// The default is stored as a string because that is what goes into the column
// and what the API exchanges; this is the one place it is turned back into a
// number, so a non-numeric default on a numeric parameter reads as 0 here and
// is caught by TestEveryParamHasAValidDefault rather than at a call site.
func (p ConfigParam) DefaultFloat() float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(p.Default), 64)
	if err != nil {
		return 0
	}
	return v
}

// sumTolerance absorbs float64 representation error. 0.15 + 0.15 + 0.30 + 0.30
// + 0.10 is not exactly 1.0 in binary floating point, so an exact comparison
// would reject the registry's own defaults.
const sumTolerance = 1e-9

// ValidateValue checks one value against its declared type and bounds,
// returning a message suitable for showing beside the field.
func (p ConfigParam) ValidateValue(raw string) error {
	value := strings.TrimSpace(raw)
	if value == "" {
		return fmt.Errorf("must not be empty")
	}

	switch p.Type {
	case ConfigTypeBoolean:
		if _, err := strconv.ParseBool(value); err != nil {
			return fmt.Errorf("must be true or false")
		}
		return nil
	case ConfigTypeString:
		return nil
	case ConfigTypeInteger:
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("must be a whole number")
		}
		return p.checkRange(float64(parsed))
	default:
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return fmt.Errorf("must be a number")
		}
		if math.IsNaN(parsed) || math.IsInf(parsed, 0) {
			return fmt.Errorf("must be a finite number")
		}
		return p.checkRange(parsed)
	}
}

func (p ConfigParam) checkRange(v float64) error {
	if p.Min != nil && v < *p.Min {
		return fmt.Errorf("must be at least %s", trimFloat(*p.Min))
	}
	if p.Max != nil && v > *p.Max {
		return fmt.Errorf("must be at most %s", trimFloat(*p.Max))
	}
	return nil
}

// ValidateConfigSet applies the cross-field rules no single parameter can
// express, over a fully-resolved view of every value (stored values with the
// pending changes already merged in).
//
// It takes the whole set rather than the changed keys because every rule here
// is about a relationship: saving one composite weight is only legal in terms
// of the other four, and a partial update that skipped the check would leave
// the weights summing to 0.9 — which silently makes every claim in the system
// score lower than it should, with nothing in the UI to say so.
func ValidateConfigSet(values map[string]string) map[string]string {
	errs := map[string]string{}

	numeric := func(key string) (float64, bool) {
		raw, ok := values[key]
		if !ok {
			return 0, false
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil {
			return 0, false
		}
		return v, true
	}

	// Rule 1: each sum group totals exactly 1.00. A group whose members are not
	// all present is skipped rather than reported: an incomplete view means the
	// caller is validating a subset, and a false failure is worse than none.
	for group, label := range sumGroupLabels {
		var sum float64
		complete := true
		for _, p := range ConfigParams {
			if p.SumGroup != group {
				continue
			}
			v, ok := numeric(p.Key)
			if !ok {
				complete = false
				break
			}
			sum += v
		}
		if complete && math.Abs(sum-1.0) > sumTolerance {
			errs[group] = fmt.Sprintf("%s must sum to 1.00, got %.4f", label, sum)
		}
	}

	// Rule 2: the velocity reference range must be a range.
	if lo, ok := numeric(SettingVelocityZMin); ok {
		if hi, ok2 := numeric(SettingVelocityZMax); ok2 && lo >= hi {
			errs[SettingVelocityZMax] = "the z-score ceiling must be greater than the floor"
		}
	}

	// Rule 3: the CSI gauge's bands must be ordered, or a score can fall in two
	// bands at once and the gauge colour becomes whichever branch runs first.
	if risky, ok := numeric(SettingCSIBandRiskyCeiling); ok {
		if watch, ok2 := numeric(SettingCSIBandWatchCeiling); ok2 && risky >= watch {
			errs[SettingCSIBandWatchCeiling] = "the watch ceiling must be greater than the risky ceiling"
		}
	}

	return errs
}

var sumGroupLabels = map[string]string{
	SumGroupCompositeWeights: "the five composite score weights",
	SumGroupHarmWeights:      "the four harm sub-weights",
	SumGroupCSIWeights:       "the two Climate Sentiment Index weights",
	SumGroupTreemapWeights:   "the two Overview ranking weights",
}

func trimFloat(v float64) string {
	return strconv.FormatFloat(v, 'g', -1, 64)
}
