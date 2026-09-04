-- ===========================================================================
-- AI SERVICE ADDITIONS FOR THE OVERVIEW PAGE AND SEGMENTED DEBUNK
-- NOT OWNED BY THIS BACKEND
-- ===========================================================================
--
-- Covers the Indonesia Climate Sentiment Index, the Overview page, and the
-- update that makes Debunk Activity per-audience-segment. Everything here is
-- AI-owned and NEEDS AI-TEAM SIGN-OFF: it is this backend's proposal for the
-- smallest schema change that makes those features computable, not an agreed
-- contract.
--
-- Same rules as 00_ai_reference_schema.sql and 01_f5_reference_schema.sql:
--
--   * THIS FILE IS NEVER EXECUTED BY THE APPLICATION.
--   * Do NOT run it against the shared Supabase database.
--   * It exists to bootstrap a blank local Postgres so the Overview endpoints
--     can be developed and tested before the AI service ships these columns.
--
-- Usage (local development only, after 00_ai_reference_schema.sql):
--   psql "postgresql://postgres:PASSWORD@localhost:5432/cis" -f docs/sql/02_f6_reference_schema.sql
--
-- ---------------------------------------------------------------------------
-- HOW THE BACKEND BEHAVES WITHOUT THESE
-- ---------------------------------------------------------------------------
--
-- All three additions are optional. The backend probes for them at boot
-- (database/migrate.go, repository.NewOverviewRepository) and degrades:
--
--   * no content_items.sentiment  -> the sentiment gauge reports status
--                                    "unavailable" with a reason naming this
--                                    file. The above/below-threshold ratio,
--                                    topic treemap and policy leaderboard are
--                                    unaffected: they are computed from claims.
--   * no content_items.city       -> the city selection labels the instance
--                                    rather than partitioning it, and the
--                                    Overview response says so
--                                    (city.partitioned=false).
--   * no claim_debunk_segments    -> the claim detail page falls back to the
--                                    single cached draft in
--                                    claims.activity_content, exactly as before.
--
-- ===========================================================================


-- ---------------------------------------------------------------------------
-- 1. content_items.sentiment
-- ---------------------------------------------------------------------------
--
-- Baseline Climate Sentiment is
--   BCS = (PositiveVolume - NegativeVolume) / TotalClimateConversationVolume
-- over ALL climate-related content, "independent of the claim repository".
--
-- `stance` cannot stand in for this. Stance is a position relative to a
-- specific claim, and it only exists for content the pipeline clustered:
-- opposing a false claim is *good* for the city's information health, so
-- reading stance as sentiment would invert the index for exactly the content
-- the product most wants to see.
--
-- Values: 'positive' | 'negative' | 'neutral'. NULL means not yet classified,
-- and NULL rows still count toward the denominator — a classification backlog
-- must not make the city look more positive than it is.
ALTER TABLE public.content_items
  ADD COLUMN IF NOT EXISTS sentiment character varying(16);

-- The index the sentiment window query needs: a 7-day rolling aggregate over a
-- table that grows with every ingested post.
CREATE INDEX IF NOT EXISTS idx_content_items_created_sentiment
  ON public.content_items (created_at, sentiment);


-- ---------------------------------------------------------------------------
-- 2. content_items.city
-- ---------------------------------------------------------------------------
--
-- The city selection configures the single Indonesian city the instance
-- monitors, and selecting a new one immediately re-scopes every Overview
-- metric. Nothing in the base schema carries a city: `location` is free text
-- written by the source platform, so it cannot be joined on.
--
-- This is the resolved city name, matching one of the names in
-- internal/models/f6_cities.go exactly (e.g. 'Jakarta', 'Makassar').
-- Normalising it on the AI side is deliberate: the backend must not be in the
-- business of geocoding free-text locations.
--
-- A claim is considered to belong to the configured city when any content item
-- backing it does; see repository.OverviewRepository.claimScope.
ALTER TABLE public.content_items
  ADD COLUMN IF NOT EXISTS city character varying(128);

CREATE INDEX IF NOT EXISTS idx_content_items_city_created
  ON public.content_items (city, created_at);


-- ---------------------------------------------------------------------------
-- 3. claim_debunk_segments
-- ---------------------------------------------------------------------------
--
-- Replaces the single generic Debunk draft with one tailored draft per
-- affected audience segment. That is one-to-many, so it cannot live in a column
-- on claims.
--
-- Generation and caching rules are unchanged from the older activity_content
-- column: every variant is generated ONCE, at claim creation, and served from
-- cache. Opening a claim detail page must never trigger a generation, and the
-- backend never writes this table.
--
-- Existing/Generic claims only. Synthetic claims carry an unsegmented Prebunk
-- draft in claims.activity_content.
CREATE TABLE IF NOT EXISTS public.claim_debunk_segments (
  id uuid NOT NULL,
  claim_id uuid NOT NULL,
  -- The segment label shown on the card ('Commuters', 'Small business owners').
  -- Every variant must be visibly attributed to its segment: a variant with no
  -- segment name reads as the generic draft this table replaces.
  segment_name character varying(255) NOT NULL,
  -- Why this segment was identified: the framing or concern the copy addresses.
  -- Rendered as the card's subtitle.
  segment_rationale text,
  content text NOT NULL,
  -- Card order, most-exposed segment first.
  rank integer NOT NULL DEFAULT 0,
  generated_at timestamp with time zone NOT NULL DEFAULT now(),
  CONSTRAINT claim_debunk_segments_pkey PRIMARY KEY (id),
  CONSTRAINT claim_debunk_segments_claim_segment_key UNIQUE (claim_id, segment_name),
  CONSTRAINT claim_debunk_segments_claim_id_fkey
    FOREIGN KEY (claim_id) REFERENCES public.claims(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_claim_debunk_segments_claim
  ON public.claim_debunk_segments (claim_id, rank);
