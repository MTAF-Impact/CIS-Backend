-- ===========================================================================
-- AI SERVICE REFERENCE SCHEMA — NOT OWNED BY THIS BACKEND
-- ===========================================================================
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

CREATE TABLE IF NOT EXISTS public.topics (
  id uuid NOT NULL,
  name character varying NOT NULL,
  description text,
  embedding vector(1536),
  created_at timestamp with time zone NOT NULL DEFAULT now(),
  updated_at timestamp with time zone NOT NULL DEFAULT now(),
  CONSTRAINT topics_pkey PRIMARY KEY (id)
);

CREATE TABLE IF NOT EXISTS public.policies (
  id uuid NOT NULL,
  title character varying NOT NULL,
  description text,
  created_at timestamp with time zone NOT NULL DEFAULT now(),
  CONSTRAINT policies_pkey PRIMARY KEY (id)
);

CREATE TABLE IF NOT EXISTS public.claims (
  id uuid NOT NULL,
  claim_type character varying NOT NULL,
  claim_statement text NOT NULL,
  topic_id uuid NOT NULL,
  status character varying NOT NULL,
  policy_id uuid,
  embedding vector(1536),
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
  created_at timestamp with time zone NOT NULL DEFAULT now(),
  updated_at timestamp with time zone NOT NULL DEFAULT now(),
  CONSTRAINT claims_pkey PRIMARY KEY (id),
  CONSTRAINT claims_topic_id_fkey FOREIGN KEY (topic_id) REFERENCES public.topics(id),
  CONSTRAINT claims_policy_id_fkey FOREIGN KEY (policy_id) REFERENCES public.policies(id)
);

CREATE TABLE IF NOT EXISTS public.claim_policies (
  claim_id uuid NOT NULL,
  policy_id uuid NOT NULL,
  CONSTRAINT claim_policies_pkey PRIMARY KEY (claim_id, policy_id),
  CONSTRAINT claim_policies_claim_id_fkey FOREIGN KEY (claim_id) REFERENCES public.claims(id),
  CONSTRAINT claim_policies_policy_id_fkey FOREIGN KEY (policy_id) REFERENCES public.policies(id)
);

CREATE TABLE IF NOT EXISTS public.content_items (
  id uuid NOT NULL,
  text text NOT NULL,
  source character varying NOT NULL,
  author_id character varying,
  location character varying,
  outrage_score double precision,
  moral_foundation character varying,
  extracted_claim text,
  underlying_grievance text,
  stance character varying,
  impressions integer,
  positive_reaction_count integer,
  negative_reaction_count integer,
  embedding vector(1536),
  claim_id uuid,
  created_at timestamp with time zone NOT NULL DEFAULT now(),
  CONSTRAINT content_items_pkey PRIMARY KEY (id),
  CONSTRAINT content_items_claim_id_fkey FOREIGN KEY (claim_id) REFERENCES public.claims(id)
);

CREATE TABLE IF NOT EXISTS public.topic_volume_buckets (
  id uuid NOT NULL,
  topic_id uuid NOT NULL,
  bucket_start timestamp with time zone NOT NULL,
  supporting_volume integer NOT NULL,
  CONSTRAINT topic_volume_buckets_pkey PRIMARY KEY (id),
  CONSTRAINT topic_volume_buckets_topic_id_fkey FOREIGN KEY (topic_id) REFERENCES public.topics(id)
);

CREATE TABLE IF NOT EXISTS public.narratives (
  id uuid NOT NULL,
  title character varying NOT NULL,
  summary text,
  growth_velocity double precision NOT NULL,
  emotional_intensity double precision NOT NULL,
  geographic_concentration double precision NOT NULL,
  fault_line_relevance double precision NOT NULL,
  overall_risk_score double precision NOT NULL,
  risk_level character varying NOT NULL,
  status character varying NOT NULL,
  created_at timestamp with time zone NOT NULL DEFAULT now(),
  updated_at timestamp with time zone NOT NULL DEFAULT now(),
  CONSTRAINT narratives_pkey PRIMARY KEY (id)
);

CREATE TABLE IF NOT EXISTS public.intervention_responses (
  id uuid NOT NULL,
  narrative_id uuid,
  response_type character varying NOT NULL,
  core_fact text,
  nuanced_flag text,
  reiterated_fact text,
  status character varying NOT NULL,
  reviewer_notes text,
  created_at timestamp with time zone NOT NULL DEFAULT now(),
  updated_at timestamp with time zone NOT NULL DEFAULT now(),
  CONSTRAINT intervention_responses_pkey PRIMARY KEY (id),
  CONSTRAINT intervention_responses_narrative_id_fkey FOREIGN KEY (narrative_id) REFERENCES public.narratives(id)
);

CREATE TABLE IF NOT EXISTS public.fault_lines (
  id uuid NOT NULL,
  community_name character varying NOT NULL,
  grievance_theme character varying NOT NULL,
  description text,
  embedding vector(1536),
  created_at timestamp with time zone NOT NULL DEFAULT now(),
  CONSTRAINT fault_lines_pkey PRIMARY KEY (id)
);

CREATE TABLE IF NOT EXISTS public.official_sources (
  id uuid NOT NULL,
  title character varying NOT NULL,
  content text NOT NULL,
  source_url character varying,
  embedding vector(1536),
  created_at timestamp with time zone NOT NULL DEFAULT now(),
  CONSTRAINT official_sources_pkey PRIMARY KEY (id)
);
