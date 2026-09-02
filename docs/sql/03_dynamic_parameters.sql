-- ===========================================================================
-- 03 - DYNAMIC PARAMETERS (cis_settings)
-- BACKEND-OWNED. Safe to run against the shared Supabase database.
-- ===========================================================================
--
-- Seeds every runtime-configurable parameter into cis_settings, the key/value
-- store the F4 Admin Setting page reads and writes.
--
-- Unlike docs/sql/00-02, THIS FILE IS MEANT TO BE RUN. It touches exactly one
-- backend-owned table (cis_settings) and creates no schema beyond it. Paste it
-- into the Supabase SQL editor and execute.
--
-- ---------------------------------------------------------------------------
-- IDEMPOTENT, AND NON-DESTRUCTIVE BY DESIGN
-- ---------------------------------------------------------------------------
--
-- Every insert is ON CONFLICT (key) DO NOTHING. Re-running it adds parameters
-- that are missing and leaves every value an operator has already tuned exactly
-- as it is. That is load-bearing rather than merely tidy: silently resetting a
-- governed parameter to its default is indistinguishable, from the outside,
-- from an admin having changed it - except that nothing would appear in
-- cis_setting_history to say who did.
--
-- To restore one parameter to its default, DELETE its row rather than
-- overwriting it (or call DELETE /api/v1/settings/parameters/{key}). A missing
-- row falls back to the documented default in internal/models/config_params.go,
-- so it follows the specification if the specification is ever revised; a row
-- holding a copy of yesterday's default would not.
--
-- ---------------------------------------------------------------------------
-- RUNNING THIS IS OPTIONAL
-- ---------------------------------------------------------------------------
--
-- The backend applies the same seed on every boot, from the same registry
-- (database/migrate.go's seedSettings, over models.ConfigParams). This script
-- exists so the rows can be created and reviewed from the Supabase console
-- without a deploy, and so the AI service has a table to read from before the
-- backend has ever started against a fresh database.
--
-- ---------------------------------------------------------------------------
-- WHO READS WHAT
-- ---------------------------------------------------------------------------
--
-- The write path is always the frontend, through the backend
-- (PUT /api/v1/settings/parameters). The read path is shared:
--
--   * the backend reads the alert threshold, the CSI parameters, the Overview
--     ranking weights, the retention horizon and the presentation weights;
--   * the AI service reads the scoring weights, the R/V normalisation
--     parameters, the NPR window and gamma, the falseness and clustering
--     thresholds, and the debunk segment cap.
--
-- The full split is in docs/local_docs/AI_DYNAMIC_PARAMETER.md and
-- docs/local_docs/FE_DYNAMIC_PARAMETER.md.
--
-- NOT HERE, deliberately:
--
--   * the ~30 F5 detector parameters - they live in cis_detector_settings as
--     typed columns, because two of their constraints are cross-field and a
--     flat key/value setter cannot check them (PRD 10.11, US62);
--   * infrastructure - ports, pool sizes, timeouts, cron expressions, storage
--     credentials. Changing those is a deployment act, and they stay in the
--     environment. See .env.example.
--
-- ---------------------------------------------------------------------------
-- CROSS-FIELD RULES THE API ENFORCES (this script cannot)
-- ---------------------------------------------------------------------------
--
--   * scoring.weight_*                       must sum to exactly 1.00
--   * scoring.harm_weight_*                  must sum to exactly 1.00
--   * csi.weight_bcs + csi.weight_risk_load  must sum to exactly 1.00
--   * overview.treemap_weight_*              must sum to exactly 1.00
--   * scoring.harm_weight_policy_disruption  hard ceiling of 0.25 - PRD 6.2.4's
--     bias guardrail against scoring criticism of a government's own policy
--     as harm
--   * scoring.velocity_zscore_max            > scoring.velocity_zscore_min
--   * csi.band_watch_ceiling                 > csi.band_risky_ceiling
--
-- Edit values through the API rather than with UPDATE statements here: the API
-- checks these rules and records who changed what in cis_setting_history. A
-- direct UPDATE does neither, and a set of weights summing to 0.9 lowers every
-- claim's score in the system with nothing on screen to say so.
--
-- ---------------------------------------------------------------------------
-- DERIVED VALUES HAVE NO ROW
-- ---------------------------------------------------------------------------
--
-- AP-20, the CSI RiskThreshold, is absent on purpose: it always equals
-- alert_threshold (AP-16), so "elevated risk" cannot come to mean one thing on
-- the Alert page and another on the Overview gauge. It is computed on read and
-- served read-only beside its source.
--
-- ===========================================================================

BEGIN;

-- cis_settings is normally created by the backend's AutoMigrate. This guard
-- lets the script run against a database the backend has never started against;
-- the column list matches internal/models/cis_tables.go's CISSetting exactly.
CREATE TABLE IF NOT EXISTS cis_settings (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    key         varchar(128) NOT NULL,
    value       text         NOT NULL,
    value_type  varchar(32)  NOT NULL,
    description text,
    updated_by  uuid,
    created_at  timestamptz  NOT NULL DEFAULT now(),
    updated_at  timestamptz  NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_cis_settings_key ON cis_settings (key);

INSERT INTO cis_settings (id, key, value, value_type, description, created_at, updated_at)
SELECT gen_random_uuid(), v.key, v.value, v.value_type, v.description, now(), now()
FROM (VALUES
-- Alerting & risk threshold (operations tier)
  ('alert_threshold', '70', 'number',
   'AP-16 — The FinalClaimScore at or above which a claim reads as Over Threshold on the Alert page, counts as above-threshold on the Overview, and can raise a threshold-crossing notification. (PRD §8; US32, US29, US71)'),

-- Overview page display (operations tier)
  ('overview.top_policy_limit', '5', 'number',
   'How many policies the Overview''s O3 leaderboard lists. (PRD §11; US70)'),
  ('overview.mom_window_days', '30', 'number',
   'The window the O2 topic modal compares against the preceding one for its ▲/▼ change figure. (PRD §11; US69)'),

-- Public Policy Bank (operations tier)
  ('policy.upload_warn_size_mb', '50', 'number',
   'AP-17 — File size above which the Add Public Policy modal warns the uploader. (PRD §7; US40)'),

-- AI content generation (operations tier)
  ('ai.debunk_segment_max_count', '3', 'number',
   'AP-21 — How many audience-segmented debunk drafts the AI generates for each claim. (PRD US12; US33)'),

-- City & locale (operations tier)
  ('monitored_city', 'Jakarta', 'string',
   'AP-22 — The single Indonesian city this instance monitors. Scopes every metric on the Overview page. (PRD US65; §6.6.4)'),
  ('city_timezone', 'Asia/Jakarta', 'string',
   'IANA zone for the city-local half of every F5 report footer timestamp. (PRD §10.8)'),

-- Claim score — composite weights (analytics tier)
  ('scoring.weight_reach', '0.15', 'number',
   'AP-01 — Share of the composite score contributed by how far the claim has travelled. (PRD §6.3; US22)'),
  ('scoring.weight_velocity', '0.15', 'number',
   'AP-02 — Share contributed by how fast the claim is currently growing. (PRD §6.3; US22)'),
  ('scoring.weight_falseness', '0.30', 'number',
   'AP-03 — Share contributed by how confidently the claim is confirmed false. (PRD §6.3; US22)'),
  ('scoring.weight_harm', '0.30', 'number',
   'AP-04 — Share contributed by the estimated real-world damage the claim could cause. (PRD §6.3; US22)'),
  ('scoring.weight_emotional_intensity', '0.10', 'number',
   'AP-05 — Share contributed by how angry the public reaction to the claim is. (PRD §6.3; US22)'),

-- Harm severity — sub-weights (analytics tier)
  ('scoring.harm_weight_public_safety', '0.35', 'number',
   'AP-06 — Share of Harm Severity carried by risk to physical safety. (PRD §6.2.4; US23)'),
  ('scoring.harm_weight_institutional_trust', '0.30', 'number',
   'AP-07 — Share carried by erosion of trust in public institutions. (PRD §6.2.4; US23)'),
  ('scoring.harm_weight_economic', '0.20', 'number',
   'AP-08 — Share carried by economic damage. (PRD §6.2.4; US23)'),
  ('scoring.harm_weight_policy_disruption', '0.15', 'number',
   'AP-09 — Share carried by concrete interference with policy execution. (PRD §6.2.4; US23)'),

-- Reach & velocity normalisation (analytics tier)
  ('scoring.reach_normalization_window_days', '90', 'number',
   'AP-10 — How far back R_min/R_max are observed before raw reach is normalised onto 0-100. (PRD §6.2.1)'),
  ('scoring.reach_weight_impressions', '0.25', 'number',
   'Weight of log(1+Impressions) inside raw reach. (PRD §6.2.1)'),
  ('scoring.reach_weight_unique_authors', '0.25', 'number',
   'Weight of log(1+UniqueAuthors) inside raw reach. (PRD §6.2.1)'),
  ('scoring.reach_weight_content_count', '0.25', 'number',
   'Weight of log(1+ContentCount) inside raw reach. (PRD §6.2.1)'),
  ('scoring.reach_weight_platform_spread', '0.25', 'number',
   'Weight of the DistinctPlatforms/TotalMonitoredPlatforms ratio inside raw reach. (PRD §6.2.1)'),
  ('scoring.velocity_interval_hours', '6', 'number',
   'AP-11 — The gap between the two volume readings whose difference is the growth rate. (PRD §6.2.2)'),
  ('scoring.velocity_zscore_min', '-3', 'number',
   'AP-12 — Standard deviations below baseline that map to a Velocity of 0. (PRD §6.2.2)'),
  ('scoring.velocity_zscore_max', '3', 'number',
   'AP-12 — Standard deviations above baseline that map to a Velocity of 100. (PRD §6.2.2)'),
  ('scoring.velocity_epsilon', '0.0001', 'number',
   'AP-13 — Division-by-zero guard for a brand-new claim with no prior volume. (PRD §6.2.2)'),

-- Public pushback discount (analytics tier)
  ('scoring.npr_window_hours', '36', 'number',
   'AP-14 — The window over which supporting and opposing volume are compared to compute NPR. (PRD §6.4.3)'),
  ('scoring.discount_gamma', '0.5', 'number',
   'AP-15 — The largest share of a claim''s score that organic pushback can remove. (PRD §6.4.4)'),
  ('scoring.npr_reliability_minimum_posts', '25', 'number',
   'Total volume below which no discount is applied, because the pushback signal is too thin to trust. (PRD §6.4.7)'),

-- Falseness matching (analytics tier)
  ('scoring.falseness_match_threshold', '0.55', 'number',
   'Minimum similarity to a verified official source before a claim is scored as false. (PRD §6.2.3)'),
  ('scoring.falseness_live_match_score', '75', 'number',
   'Falseness score assigned when the live Fact Check API returns a false verdict. (PRD §6.2.3)'),

-- Clustering & policy matchmaking (analytics tier)
  ('clustering.claim_attach_threshold', '0.55', 'number',
   'Similarity at which a new post joins an existing claim rather than seeding a new one. (PRD §6.2.1)'),
  ('clustering.topic_attach_threshold', '0.5', 'number',
   'Similarity at which a new claim joins an existing topic rather than creating one. (PRD US42)'),
  ('matchmaking.claim_prefilter_threshold', '0.35', 'number',
   'Similarity a claim must reach to be sent to the LLM as a candidate match for a policy. (PRD US42)'),

-- Climate Sentiment Index (analytics tier)
  ('csi.weight_bcs', '0.5', 'number',
   'AP-18 — Share of the index carried by the overall tone of the climate conversation. (PRD §6.6; US67, US68)'),
  ('csi.weight_risk_load', '0.5', 'number',
   'AP-19 — Share carried by the burden of serious claims on the conversation. (PRD §6.6; US67, US68)'),
  ('csi.window_days', '7', 'number',
   'The rolling average behind the headline gauge figure. (PRD §6.6.3)'),
  ('csi.momentum_lag_hours', '24', 'number',
   'How far behind the headline window the comparison window sits, giving the direction arrow. (PRD §6.6.3)'),
  ('csi.minimum_volume', '100', 'number',
   'Conversation volume below which the gauge reads "Insufficient Data" instead of a score. (PRD §6.6.3)'),
  ('csi.band_risky_ceiling', '33.33', 'number',
   'Index value below which the gauge shows red. (PRD §6.6.5; US68)'),
  ('csi.band_watch_ceiling', '66.67', 'number',
   'Index value below which the gauge shows amber, and at or above which it shows green. (PRD §6.6.5; US68)'),

-- Overview ranking formula (analytics tier)
  ('overview.treemap_weight_above_count', '0.5', 'number',
   'Share of a topic''s treemap box size, and a policy''s leaderboard rank, driven by how many of its claims are above threshold. (PRD §11; US69, US70)'),
  ('overview.treemap_weight_avg_score', '0.5', 'number',
   'Share driven by the average score of its claims. (PRD §11; US69, US70)'),

-- History retention (analytics tier)
  ('alerts.score_snapshot_retention_days', '400', 'number',
   'How long per-claim score snapshots are kept before the hourly job prunes them. (PRD US27)')
) AS v(key, value, value_type, description)
ON CONFLICT (key) DO NOTHING;

COMMIT;

-- ---------------------------------------------------------------------------
-- VERIFY
-- ---------------------------------------------------------------------------
--
-- Everything configured, grouped by the prefix that names its area:
--
--   SELECT split_part(key, '.', 1) AS area, count(*)
--   FROM cis_settings
--   GROUP BY 1 ORDER BY 1;
--
-- The four sums the API enforces, so a hand-edited database can be checked
-- before the drift is noticed as a scoring change nobody made:
--
--   SELECT 'composite' AS weights, sum(value::numeric) FROM cis_settings
--     WHERE key LIKE 'scoring.weight_%'
--   UNION ALL
--   SELECT 'harm', sum(value::numeric) FROM cis_settings
--     WHERE key LIKE 'scoring.harm_weight_%'
--   UNION ALL
--   SELECT 'csi', sum(value::numeric) FROM cis_settings
--     WHERE key LIKE 'csi.weight_%'
--   UNION ALL
--   SELECT 'overview', sum(value::numeric) FROM cis_settings
--     WHERE key LIKE 'overview.treemap_weight_%';
--
-- Each row must read exactly 1.000.
--
-- Who changed what, newest first (US62's audit trail, shared with the F5
-- detector parameters):
--
--   SELECT h.key, h.from_value, h.to_value, u.email, h.created_at
--   FROM cis_setting_history h
--   LEFT JOIN cis_users u ON u.id = h.changed_by
--   ORDER BY h.created_at DESC
--   LIMIT 50;
