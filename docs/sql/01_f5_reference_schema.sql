-- ===========================================================================
-- F5 COORDINATED-NETWORK DETECTOR — AI SERVICE REFERENCE SCHEMA
-- NOT OWNED BY THIS BACKEND
-- ===========================================================================
--
-- Derived from PRD v1.4 Section 10.10 on 2026-08-31, plus the columns the rest
-- of Section 10 requires but 10.10 declares no home for. Those are marked
-- "BEYOND 10.10" with the gap number from docs/local_docs/PRD-v1.4.md 8.3, and
-- they NEED AI-TEAM SIGN-OFF — they are this backend's proposal, not an agreed
-- contract. The Go structs in internal/models/f5_ai_tables.go carry the same
-- markers against the same columns.
--
-- Same rules as 00_ai_reference_schema.sql, and for the same reasons:
--
--   * THIS FILE IS NEVER EXECUTED BY THE APPLICATION.
--   * Do NOT run it against the shared Supabase database. If the AI team has
--     built the pipeline, these tables already exist there and are theirs.
--   * Do NOT treat it as the source of truth. It exists to bootstrap a blank
--     local Postgres so the F5 endpoints can be developed and tested before the
--     detection pipeline is available.
--   * The backend's own F5 tables (cis_network_reviews, cis_network_review_log,
--     cis_coordination_allowlist, cis_common_phrases, cis_network_reports,
--     cis_export_audit_log, cis_detector_settings, cis_setting_history) are
--     created by GORM AutoMigrate on boot and are deliberately absent here.
--
-- Usage (local development only):
--   psql "postgresql://postgres:PASSWORD@localhost:5432/cis" -f docs/sql/01_f5_reference_schema.sql
--
-- Two schema conventions worth knowing before reading:
--
--   1. List columns are `jsonb`, not `text[]`. The backend may connect through
--      Supabase's transaction-mode pooler with PreferSimpleProtocol enabled,
--      where a Postgres array arrives as an unparsed `{a,b,c}` literal. jsonb
--      round-trips identically under both protocols. See models.StringList.
--   2. `review_status` is NOT a column on coordinated_network, though PRD 10.10
--      declares one. A human's assessment on a table the pipeline rewrites would
--      be erased by the next detection run, so it lives in the backend-owned
--      cis_network_reviews overlay — the same shape, and the same reason, as
--      cis_claim_reviews. Likewise `coordination_allowlist` is backend-owned:
--      it records human decisions and the pipeline READS it. See
--      docs/local_docs/PRD-v1.4.md 6.1 and 6.2.
--
-- See docs/DATABASE.md for the full table-ownership matrix.
-- ===========================================================================

-- One execution of the pipeline over a defined scope and window (PRD 10.5.8).
--
-- parameters_json is load-bearing rather than diagnostic: US62 requires that
-- changing a detector parameter never retroactively alters a stored detection,
-- so a report generated months later reads its configuration from this row and
-- not from the current settings.
CREATE TABLE IF NOT EXISTS public.detection_run (
  run_id uuid NOT NULL,
  -- The claims the run was anchored to: one for a claim-scoped run, many for a
  -- topic batch (PRD 10.5.1). JSON array of uuid strings.
  scope_claim_ids jsonb NOT NULL DEFAULT '[]'::jsonb,
  -- scheduled | velocity | on_demand
  trigger_source character varying(32) NOT NULL,
  window_start timestamp with time zone NOT NULL,
  window_end timestamp with time zone NOT NULL,
  -- The whole parameter set in force when the run executed.
  parameters_json jsonb,
  -- Model and library identities, for reproducibility (PRD 10.9.1 rule 6).
  model_versions jsonb,
  random_seed bigint,
  library_version character varying(255),
  -- Signal families that could not be measured this run. Two or more caps every
  -- network in the run at Medium confidence (PRD 10.6.3 rule 4) — which is why
  -- it is stored at run level: "why is everything Medium this week?" is a
  -- question about the run, not about any one network.
  signals_unavailable jsonb NOT NULL DEFAULT '[]'::jsonb,
  -- |A| exceeded A_max and the candidate set was cut. Displayed on the network
  -- detail page, not merely stored: a truncated run has known incomplete recall
  -- and the analyst has to be told at the point of judgement (PRD 10.5.1).
  truncated_bool boolean NOT NULL DEFAULT false,
  candidates_count integer NOT NULL DEFAULT 0,
  -- pending | running | completed | failed
  status character varying(32) NOT NULL DEFAULT 'pending',
  error text,
  started_at timestamp with time zone NOT NULL DEFAULT now(),
  completed_at timestamp with time zone,
  CONSTRAINT detection_run_pkey PRIMARY KEY (run_id)
);

-- The scheduler asks "when did the last scheduled sweep start?" on every tick,
-- and the run history is always read newest-first.
CREATE INDEX IF NOT EXISTS detection_run_trigger_started_idx
  ON public.detection_run (trigger_source, started_at DESC);
CREATE INDEX IF NOT EXISTS detection_run_started_idx
  ON public.detection_run (started_at DESC);

-- One detected cluster: the user-facing object of F5.
CREATE TABLE IF NOT EXISTS public.coordinated_network (
  network_id uuid NOT NULL,
  run_id uuid NOT NULL,
  label character varying(255) NOT NULL,
  -- Cluster metrics (PRD 10.5.5), each on 0-100.
  coordination_score double precision NOT NULL DEFAULT 0,
  sy double precision NOT NULL DEFAULT 0,  -- Synchrony
  du double precision NOT NULL DEFAULT 0,  -- Duplication
  co double precision NOT NULL DEFAULT 0,  -- Cohesion
  pr double precision NOT NULL DEFAULT 0,  -- Provenance anomaly
  au double precision NOT NULL DEFAULT 0,  -- Automation & behavioural anomaly
  -- Count of distinct signal families independently scoring >= 50 (PRD 10.4).
  -- Computed by the pipeline and stored, never recomputed by the backend: it is
  -- printed in every PDF, and a value derived under a different reading of
  -- "signal family" would not match the report it appeared in. See
  -- docs/local_docs/PRD-v1.4.md open question 7 — this definition must be
  -- frozen before any report ships.
  signal_breadth integer NOT NULL DEFAULT 0,
  -- low | medium | high (PRD 10.6.2). Computed, never set by a human.
  confidence_band character varying(16) NOT NULL DEFAULT 'low',
  -- The raw integer observation behind each metric. US50 requires the counts,
  -- not just the normalised scores: "43 of 47 accounts posted within the same
  -- 6-minute window, 3 times in 24h".
  raw_counts_json jsonb,
  account_count integer NOT NULL DEFAULT 0,
  post_count integer NOT NULL DEFAULT 0,
  platforms jsonb NOT NULL DEFAULT '[]'::jsonb,
  internal_density double precision NOT NULL DEFAULT 0,
  conductance double precision NOT NULL DEFAULT 0,
  -- BEYOND 10.10 (gap 7). Genuine unclustered accounts active on the same
  -- claim, rendered for contrast in the graph (US51) and counted in the report
  -- (PRD 10.8 item 5). The contrast set is what lets an analyst see that the
  -- cluster is unusual relative to the ordinary conversation.
  comparison_account_count integer NOT NULL DEFAULT 0,
  -- Recurrence (PRD 10.5.7): a cluster whose member-set Jaccard against a
  -- stored fingerprint is >= 0.50 is the same network resurfacing and inherits
  -- its history through the parent chain.
  fingerprint_hash character varying(128) NOT NULL DEFAULT '',
  parent_network_id uuid,
  -- Membership >= 60% allowlisted (PRD 10.6.3 rule 3): suppressed entirely, on
  -- every surface. Stored rather than recomputed so a given detection's
  -- suppression is stable as the allowlist changes underneath it.
  allowlist_suppressed boolean NOT NULL DEFAULT false,
  -- US56 retroactively marked this network after a member was allowlisted.
  relabelled boolean NOT NULL DEFAULT false,
  created_at timestamp with time zone NOT NULL DEFAULT now(),
  CONSTRAINT coordinated_network_pkey PRIMARY KEY (network_id),
  CONSTRAINT coordinated_network_run_fkey
    FOREIGN KEY (run_id) REFERENCES public.detection_run (run_id) ON DELETE CASCADE,
  CONSTRAINT coordinated_network_parent_fkey
    FOREIGN KEY (parent_network_id) REFERENCES public.coordinated_network (network_id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS coordinated_network_run_idx
  ON public.coordinated_network (run_id);
CREATE INDEX IF NOT EXISTS coordinated_network_score_idx
  ON public.coordinated_network (coordination_score DESC);
CREATE INDEX IF NOT EXISTS coordinated_network_band_idx
  ON public.coordinated_network (confidence_band);
CREATE INDEX IF NOT EXISTS coordinated_network_fingerprint_idx
  ON public.coordinated_network (fingerprint_hash);

-- The durable social-media account entity.
--
-- PRD 10.4 constrains what may be stored: "public handle and platform-issued ID
-- only". There is deliberately no real-name column, no inferred identity, and no
-- bot verdict — governance rules 2 and 3 (PRD 10.9.1). Do not add one.
CREATE TABLE IF NOT EXISTS public.account (
  account_id uuid NOT NULL,
  platform character varying(64) NOT NULL,
  platform_account_id character varying(255) NOT NULL,
  handle character varying(255) NOT NULL,
  -- The account's own creation date, feeding the creation-time proximity
  -- sub-signal of w_meta (PRD 10.5.2.4).
  created_at_platform timestamp with time zone,
  -- Perceptual hash (pHash) of the profile image.
  profile_hash character varying(128),
  -- BEYOND 10.10 (gap 1). PRD 10.5.2.4 lists five w_meta sub-signals; 10.10's
  -- account table declares columns for three of them. Without these, bio
  -- similarity and declared-location/client-string identity have no source at
  -- all and w_meta is computed from a fraction of its stated inputs.
  bio text,
  declared_location character varying(255),
  client_app character varying(255),
  first_seen timestamp with time zone NOT NULL DEFAULT now(),
  last_seen timestamp with time zone NOT NULL DEFAULT now(),
  CONSTRAINT account_pkey PRIMARY KEY (account_id),
  CONSTRAINT account_platform_unique UNIQUE (platform, platform_account_id)
);

CREATE INDEX IF NOT EXISTS account_handle_idx ON public.account (lower(handle));

-- One account's membership of one network, with its individual behavioural
-- metrics. PRD 10.10: the account is durable, the membership is per-detection.
CREATE TABLE IF NOT EXISTS public.network_account (
  network_id uuid NOT NULL,
  account_id uuid NOT NULL,
  -- BEYOND 10.10 (gap 7). member | comparison. Separates the cluster from the
  -- contrast set; see coordinated_network.comparison_account_count.
  membership_role character varying(16) NOT NULL DEFAULT 'member',
  posts_in_cluster integer NOT NULL DEFAULT 0,
  duplication_rate double precision NOT NULL DEFAULT 0,
  median_interpost_interval_seconds double precision,
  circadian_coverage double precision NOT NULL DEFAULT 0,
  degree_centrality double precision NOT NULL DEFAULT 0,
  eigenvector_centrality double precision NOT NULL DEFAULT 0,
  -- This account's individual contribution to each cluster metric
  -- (PRD 10.5.6 item 4).
  score_contribution_json jsonb,
  -- BEYOND 10.10 (gap 4). Precomputed ForceAtlas2 coordinates. PRD 10.5.6
  -- item 5 requires them "so the UI and the PDF render identically", and
  -- PRD 10.8 requires byte-identical report regeneration. A force-directed
  -- layout lands somewhere different on every run, so this is the one piece of
  -- the snapshot that genuinely cannot be recomputed at render time.
  layout_x double precision,
  layout_y double precision,
  CONSTRAINT network_account_pkey PRIMARY KEY (network_id, account_id),
  CONSTRAINT network_account_network_fkey
    FOREIGN KEY (network_id) REFERENCES public.coordinated_network (network_id) ON DELETE CASCADE,
  CONSTRAINT network_account_account_fkey
    FOREIGN KEY (account_id) REFERENCES public.account (account_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS network_account_account_idx
  ON public.network_account (account_id);

-- One retained behavioural edge with its per-family decomposition.
--
-- This table is what makes membership explainable. PRD 10.5.3: "Every retained
-- edge stores its per-family decomposition, so any account's membership can be
-- explained down to the specific behaviours that connected it." US55 turns that
-- into a hard product rule — no account may appear in a network without a
-- viewable reason.
CREATE TABLE IF NOT EXISTS public.network_edge (
  network_id uuid NOT NULL,
  account_a uuid NOT NULL,
  account_b uuid NOT NULL,
  w_total double precision NOT NULL DEFAULT 0,
  w_time double precision NOT NULL DEFAULT 0,    -- temporal synchrony
  w_text double precision NOT NULL DEFAULT 0,    -- content duplication
  w_amp double precision NOT NULL DEFAULT 0,     -- co-amplification
  w_meta double precision NOT NULL DEFAULT 0,    -- provenance
  w_struct double precision NOT NULL DEFAULT 0,  -- structural overlap
  -- How many families cleared 0.25 on this edge. The multi-signal rule
  -- (PRD 10.5.3) requires at least two, and it is the pipeline's primary
  -- false-positive control: "Synchrony alone is a timezone. Duplication alone
  -- is a hashtag. Provenance alone is a signup surge. Only their conjunction is
  -- evidence."
  signal_count integer NOT NULL DEFAULT 0,
  CONSTRAINT network_edge_pkey PRIMARY KEY (network_id, account_a, account_b),
  CONSTRAINT network_edge_network_fkey
    FOREIGN KEY (network_id) REFERENCES public.coordinated_network (network_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS network_edge_account_a_idx ON public.network_edge (network_id, account_a);
CREATE INDEX IF NOT EXISTS network_edge_account_b_idx ON public.network_edge (network_id, account_b);

-- The immutable, hashed capture of one post as it existed at detection time.
--
-- PRD 10.5.6: operators delete their own content once a campaign concludes, so a
-- report built from live data two weeks later documents an empty set. US54
-- renders from this table and never re-fetches, which is why a deleted post can
-- still be shown, marked "no longer publicly available".
CREATE TABLE IF NOT EXISTS public.network_evidence_post (
  evidence_id uuid NOT NULL,
  network_id uuid NOT NULL,
  account_id uuid NOT NULL,
  post_platform_id character varying(255) NOT NULL,
  captured_text text NOT NULL,
  -- Two different times, and two columns for that reason: posted_at is when the
  -- account published, captured_at is when the pipeline snapshotted it. EVERY
  -- temporal signal depends on the first, and today's content_items table has
  -- only a capture time — see docs/local_docs/PRD-v1.4.md Section 5, the
  -- blocker.
  posted_at timestamp with time zone NOT NULL,
  captured_at timestamp with time zone NOT NULL DEFAULT now(),
  content_sha256 character varying(64) NOT NULL,
  duplicate_group_id uuid,
  is_canonical boolean NOT NULL DEFAULT false,
  still_public_bool boolean NOT NULL DEFAULT true,
  -- BEYOND 10.10. The span this variant shares with its group's canonical text.
  -- US54 requires the shared span highlighted and PRD 10.8 item 6 requires the
  -- same highlighting in the PDF, so the offsets are computed once and stored
  -- rather than derived in the browser — which would leave the report unable to
  -- reproduce them.
  shared_span_start integer,
  shared_span_end integer,
  CONSTRAINT network_evidence_post_pkey PRIMARY KEY (evidence_id),
  CONSTRAINT network_evidence_post_network_fkey
    FOREIGN KEY (network_id) REFERENCES public.coordinated_network (network_id) ON DELETE CASCADE,
  CONSTRAINT network_evidence_post_account_fkey
    FOREIGN KEY (account_id) REFERENCES public.account (account_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS network_evidence_post_network_idx
  ON public.network_evidence_post (network_id, posted_at);
CREATE INDEX IF NOT EXISTS network_evidence_post_group_idx
  ON public.network_evidence_post (network_id, duplicate_group_id);

-- One bin of the burst timeline (US53, PRD 10.5.6 item 2).
CREATE TABLE IF NOT EXISTS public.network_burst_bin (
  network_id uuid NOT NULL,
  bin_start timestamp with time zone NOT NULL,
  bin_width_seconds integer NOT NULL,
  post_count integer NOT NULL DEFAULT 0,
  zscore double precision NOT NULL DEFAULT 0,
  is_anomalous boolean NOT NULL DEFAULT false,
  CONSTRAINT network_burst_bin_pkey PRIMARY KEY (network_id, bin_start),
  CONSTRAINT network_burst_bin_network_fkey
    FOREIGN KEY (network_id) REFERENCES public.coordinated_network (network_id) ON DELETE CASCADE
);

-- The many-to-many network<->claim relation, carrying the claim-relevance
-- gate's verdict (PRD 10.5.1a).
--
-- Exactly one row per network has is_primary_claim = true: the claim with the
-- highest overlap_ratio. passed_relevance_gate is the first of US61's four
-- conditions for showing a cross-link on an F1 claim page.
CREATE TABLE IF NOT EXISTS public.network_claim_link (
  network_id uuid NOT NULL,
  claim_id uuid NOT NULL,
  -- C's posts in this claim's supporting cluster over C's posts across all
  -- monitored content in W. It answers a question no signal score can: not "is
  -- this coordinated?" but "is this coordinated *about our claim*?".
  overlap_ratio double precision NOT NULL DEFAULT 0,
  -- Share of members with >= 2 posts in the claim cluster.
  anchoring_share double precision NOT NULL DEFAULT 0,
  claim_cluster_post_count integer NOT NULL DEFAULT 0,
  is_primary_claim boolean NOT NULL DEFAULT false,
  passed_relevance_gate boolean NOT NULL DEFAULT false,
  CONSTRAINT network_claim_link_pkey PRIMARY KEY (network_id, claim_id),
  CONSTRAINT network_claim_link_network_fkey
    FOREIGN KEY (network_id) REFERENCES public.coordinated_network (network_id) ON DELETE CASCADE
);

-- The US61 badge lookup joins from a page of claim ids into this table.
CREATE INDEX IF NOT EXISTS network_claim_link_claim_idx
  ON public.network_claim_link (claim_id) WHERE passed_relevance_gate;

-- A genuinely coordinated cluster that failed the claim-relevance gate.
--
-- PRD 10.5.1a: these are real coordinated clusters — spam rings, engagement
-- farms, unrelated political amplification — that happened to pass through the
-- claim. They are not the city's problem and must NEVER appear in a climate
-- report. They are retained only so an admin can see whether omega_min is set
-- too loose or too tight (US62), which is the single read-only surface they are
-- ever exposed on.
CREATE TABLE IF NOT EXISTS public.offtopic_cluster (
  cluster_id uuid NOT NULL,
  run_id uuid NOT NULL,
  claim_id uuid NOT NULL,
  coordination_signals_json jsonb,
  overlap_ratio double precision NOT NULL DEFAULT 0,
  anchoring_share double precision NOT NULL DEFAULT 0,
  account_count integer NOT NULL DEFAULT 0,
  post_count integer NOT NULL DEFAULT 0,
  fingerprint_hash character varying(128) NOT NULL DEFAULT '',
  -- anchoring | evidence_volume | link_strength: which of the three gate tests
  -- rejected it. This is the whole diagnostic value of the table.
  failed_test character varying(32) NOT NULL,
  created_at timestamp with time zone NOT NULL DEFAULT now(),
  CONSTRAINT offtopic_cluster_pkey PRIMARY KEY (cluster_id),
  CONSTRAINT offtopic_cluster_run_fkey
    FOREIGN KEY (run_id) REFERENCES public.detection_run (run_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS offtopic_cluster_run_idx ON public.offtopic_cluster (run_id);

-- BEYOND 10.10 (gap 8). A first-class identity and digest for the evidence
-- captured for one network.
--
-- PRD 10.8 item 10 requires the report's chain-of-custody section to print
-- "evidence snapshot ID, snapshot hash, detection run ID, and the export audit
-- entry ID". Section 10.10 supplies the run id and the audit id and gives a
-- per-POST content_sha256, but declares no snapshot entity, no snapshot id, and
-- no digest over the snapshot as a whole — the snapshot is an implicit set of
-- rows across seven tables. Without this row the chain of custody has three of
-- its four fields, and US60's integrity claim ("the manifest hashes establish
-- that the bundle was not modified after generation") covers the files but not
-- the evidence they were built from.
CREATE TABLE IF NOT EXISTS public.evidence_snapshot (
  snapshot_id uuid NOT NULL,
  network_id uuid NOT NULL,
  run_id uuid NOT NULL,
  -- Computed over a canonical serialisation of every evidence row belonging to
  -- this network.
  snapshot_sha256 character varying(64) NOT NULL,
  evidence_post_count integer NOT NULL DEFAULT 0,
  created_at timestamp with time zone NOT NULL DEFAULT now(),
  -- PRD 10.9.1 rule 7's default 24-month retention. The backend's purge job
  -- selects on this date and clears it once a report has been generated from
  -- the snapshot, because the snapshot must then live as long as the report: a
  -- report whose evidence has been purged is worthless as evidence.
  expires_at timestamp with time zone,
  CONSTRAINT evidence_snapshot_pkey PRIMARY KEY (snapshot_id),
  CONSTRAINT evidence_snapshot_network_unique UNIQUE (network_id),
  CONSTRAINT evidence_snapshot_network_fkey
    FOREIGN KEY (network_id) REFERENCES public.coordinated_network (network_id) ON DELETE CASCADE,
  CONSTRAINT evidence_snapshot_run_fkey
    FOREIGN KEY (run_id) REFERENCES public.detection_run (run_id) ON DELETE CASCADE
);

-- The retention sweep scans for snapshots whose date has passed.
CREATE INDEX IF NOT EXISTS evidence_snapshot_expires_idx
  ON public.evidence_snapshot (expires_at) WHERE expires_at IS NOT NULL;
