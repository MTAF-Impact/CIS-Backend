-- ===========================================================================
-- AI SERVICE REFERENCE SCHEMA — NOT OWNED BY THIS BACKEND
-- ===========================================================================
--
-- Transcribed from the AI service's docs/DATA_MODEL.md on 2026-08-31.
-- When the AI team changes their schema, re-transcribe from that document (or
-- better, from a `pg_dump --schema-only` of the AI-owned tables) and update the
-- date above, so drift is visible instead of silent.
--
-- These tables belong to the separately-developed AI service. They are
-- reproduced here for ONE reason: bootstrapping a blank local Postgres so the
-- Go backend can be developed and tested without the AI pipeline running.
--
-- THIS FILE IS NEVER EXECUTED BY THE APPLICATION.
--
--   * Do NOT run this against the shared Supabase database. The AI team owns
--     these tables there and has already created them.
--   * Do NOT treat this as the source of truth. If the AI team changes their
--     schema, they change it; this copy is only a convenience.
--   * The backend's own tables (all prefixed `cis_`) are created by GORM
--     AutoMigrate on boot and are deliberately absent from this file.
--
-- Usage (local development only):
--   psql "postgresql://postgres:PASSWORD@localhost:5432/cis" -f docs/sql/00_ai_reference_schema.sql
--
-- See docs/DATABASE.md for the full table-ownership matrix.
-- ===========================================================================

CREATE EXTENSION IF NOT EXISTS vector;

-- The `embedding` columns below use pgvector. The Go models deliberately do
-- NOT map them, which is why AutoMigrate must never be pointed at these tables:
-- it would recreate them without the embeddings and break the AI pipeline.
--
-- The dimension is 384, matching sentence-transformers/all-MiniLM-L6-v2 and the
-- AI service's EMBEDDING_DIM default. It is NOT 1536 — a column sized for
-- OpenAI embeddings will reject every vector the AI service writes.

CREATE TABLE IF NOT EXISTS public.topics (
  id uuid NOT NULL,
  name character varying(255) NOT NULL,
  description text,
  embedding vector(384),
  created_at timestamp with time zone NOT NULL DEFAULT now(),
  updated_at timestamp with time zone NOT NULL DEFAULT now(),
  CONSTRAINT topics_pkey PRIMARY KEY (id)
);

CREATE TABLE IF NOT EXISTS public.policies (
  id uuid NOT NULL,
  title character varying(255) NOT NULL,
  description text,
  -- Text pulled out of the uploaded document; the matchmaking pipeline's
  -- grounding input. NULL when extraction failed or no document was supplied.
  extracted_text text,
  file_name character varying(255),
  file_content_type character varying(100),
  -- The raw document bytes, stored inline. An MVP simplification on the AI
  -- side: it has no object-storage credentials of its own. The backend stores
  -- its own copy in Supabase Storage and sends a signed URL instead.
  file_data bytea,
  -- NOT NULL. An insert that omits this fails outright, which is what makes a
  -- local Flow 1 impossible against a schema that leaves the column out.
  rolled_out_date date NOT NULL,
  embedding vector(384),
  -- true while the matchmaking background job is running.
  processing boolean NOT NULL DEFAULT true,
  -- Soft reference (no FK) to the backend's cis_policies.id, set only for rows
  -- created through the Flow 1 webhook. Unique, so a retried webhook is
  -- recognised as a duplicate rather than creating a second policy.
  backend_policy_id uuid,
  created_at timestamp with time zone NOT NULL DEFAULT now(),
  CONSTRAINT policies_pkey PRIMARY KEY (id),
  CONSTRAINT policies_backend_policy_id_key UNIQUE (backend_policy_id)
);

-- `status` (rolled_out / not_rolled_out) is deliberately NOT a column on the AI
-- side: it is computed from rolled_out_date on every read. The backend does
-- store a status column on cis_policies, re-derived daily by its own cron job.

CREATE TABLE IF NOT EXISTS public.claims (
  id uuid NOT NULL,
  claim_type character varying(16) NOT NULL,
  claim_statement text NOT NULL,
  topic_id uuid NOT NULL,
  status character varying(16) NOT NULL DEFAULT 'unreviewed',
  policy_id uuid,
  embedding vector(384),
  first_caught_at timestamp with time zone NOT NULL,
  reach_score double precision,
  velocity_score double precision,
  falseness_score double precision,
  harm_score double precision,
  harm_public_safety double precision,
  harm_institutional_trust double precision,
  harm_economic double precision,
  harm_policy_disruption double precision,
  harm_human_confirmed boolean NOT NULL DEFAULT false,
  emotional_intensity_score double precision,
  emotional_intensity_opposing double precision,
  claim_score double precision,
  npr double precision,
  discount_factor double precision,
  final_claim_score double precision,
  is_dormant boolean NOT NULL DEFAULT false,
  activity_content text,
  activity_generated_at timestamp with time zone,
  -- The Truth Sandwich's three blocks, split out of activity_content so the
  -- frontend can render three labelled sections. Existing claims only.
  debunk_core_fact text,
  debunk_nuanced_flag text,
  debunk_reiterated_fact text,
  created_at timestamp with time zone NOT NULL DEFAULT now(),
  updated_at timestamp with time zone NOT NULL DEFAULT now(),
  CONSTRAINT claims_pkey PRIMARY KEY (id),
  CONSTRAINT claims_topic_id_fkey FOREIGN KEY (topic_id) REFERENCES public.topics(id) ON DELETE RESTRICT,
  CONSTRAINT claims_policy_id_fkey FOREIGN KEY (policy_id) REFERENCES public.policies(id) ON DELETE SET NULL
);

-- Note: claims.status is never written by this backend and never changed by the
-- AI pipeline after creation, so it stays 'unreviewed' in practice.
-- cis_claim_reviews is the sole authority on review status. See DATABASE.md.

CREATE TABLE IF NOT EXISTS public.claim_policies (
  claim_id uuid NOT NULL,
  policy_id uuid NOT NULL,
  CONSTRAINT claim_policies_pkey PRIMARY KEY (claim_id, policy_id),
  CONSTRAINT claim_policies_claim_id_fkey FOREIGN KEY (claim_id) REFERENCES public.claims(id) ON DELETE CASCADE,
  CONSTRAINT claim_policies_policy_id_fkey FOREIGN KEY (policy_id) REFERENCES public.policies(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS public.content_items (
  id uuid NOT NULL,
  text text NOT NULL,
  source character varying(32) NOT NULL DEFAULT 'other',
  author_id character varying(255),
  location character varying(255),
  outrage_score double precision,
  moral_foundation character varying(32),
  extracted_claim text,
  underlying_grievance text,
  stance character varying(16),
  impressions integer,
  positive_reaction_count integer,
  negative_reaction_count integer,
  embedding vector(384),
  claim_id uuid,
  created_at timestamp with time zone NOT NULL DEFAULT now(),
  CONSTRAINT content_items_pkey PRIMARY KEY (id),
  CONSTRAINT content_items_claim_id_fkey FOREIGN KEY (claim_id) REFERENCES public.claims(id) ON DELETE SET NULL
);

CREATE TABLE IF NOT EXISTS public.topic_volume_buckets (
  id uuid NOT NULL,
  topic_id uuid NOT NULL,
  bucket_start timestamp with time zone NOT NULL,
  supporting_volume integer NOT NULL DEFAULT 0,
  CONSTRAINT topic_volume_buckets_pkey PRIMARY KEY (id),
  CONSTRAINT topic_volume_buckets_topic_id_key UNIQUE (topic_id, bucket_start),
  CONSTRAINT topic_volume_buckets_topic_id_fkey FOREIGN KEY (topic_id) REFERENCES public.topics(id) ON DELETE CASCADE
);

-- ---------------------------------------------------------------------------
-- The three tables below duplicate state the backend also keeps, in cis_*.
-- The backend's copies are authoritative for everything the frontend sees; the
-- AI service's exist for its own standalone admin panel. See DATABASE.md,
-- "Duplicated state".
--
-- claim_score_snapshots is the one exception: the backend DOES read it, unioned
-- with its own snapshots, because the AI service appends a row for every claim
-- it rescores while the backend only samples watched ones.
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS public.claim_alerts (
  claim_id uuid NOT NULL,
  added_at timestamp with time zone NOT NULL DEFAULT now(),
  CONSTRAINT claim_alerts_pkey PRIMARY KEY (claim_id),
  CONSTRAINT claim_alerts_claim_id_fkey FOREIGN KEY (claim_id) REFERENCES public.claims(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS public.claim_score_snapshots (
  id uuid NOT NULL,
  claim_id uuid NOT NULL,
  final_claim_score double precision NOT NULL,
  recorded_at timestamp with time zone NOT NULL DEFAULT now(),
  CONSTRAINT claim_score_snapshots_pkey PRIMARY KEY (id),
  CONSTRAINT claim_score_snapshots_claim_id_fkey FOREIGN KEY (claim_id) REFERENCES public.claims(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS public.admin_settings (
  id integer NOT NULL DEFAULT 1,
  over_threshold double precision NOT NULL DEFAULT 70.0,
  updated_at timestamp with time zone NOT NULL DEFAULT now(),
  CONSTRAINT admin_settings_pkey PRIMARY KEY (id)
);

CREATE TABLE IF NOT EXISTS public.fault_lines (
  id uuid NOT NULL,
  community_name character varying(255) NOT NULL,
  grievance_theme character varying(255) NOT NULL,
  description text,
  embedding vector(384),
  created_at timestamp with time zone NOT NULL DEFAULT now(),
  CONSTRAINT fault_lines_pkey PRIMARY KEY (id)
);

CREATE TABLE IF NOT EXISTS public.official_sources (
  id uuid NOT NULL,
  title character varying(255) NOT NULL,
  content text NOT NULL,
  source_url character varying(1024),
  embedding vector(384),
  created_at timestamp with time zone NOT NULL DEFAULT now(),
  CONSTRAINT official_sources_pkey PRIMARY KEY (id)
);

-- Removed on 2026-08-31: `narratives` and `intervention_responses`. Neither is
-- in the AI service's model set any more. The Truth Sandwich fields that used
-- to look like they lived on intervention_responses are the claims.debunk_*
-- columns above.
