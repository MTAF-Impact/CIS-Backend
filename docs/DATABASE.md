# Database & Table Ownership

The CIS database is a **single Supabase Postgres shared by two independently
developed services**: the AI service (already in production) and this Go
backend.

Everything about the schema design follows from one rule.

---

## The rule

> **This backend never reads-modify-writes an AI-owned table. It only ever
> SELECTs from them. Every table it writes is prefixed `cis_`.**

| Ownership | Tables | This backend's access |
|---|---|---|
| **AI service** | `claims`, `content_items`, `topics`, `policies`, `claim_policies`, `topic_volume_buckets`, `narratives`, `intervention_responses`, `fault_lines`, `official_sources` | **SELECT only.** Never inserted, updated, deleted, or migrated. |
| **This backend** | `cis_users`, `cis_refresh_tokens`, `cis_policies`, `cis_claim_reviews`, `cis_claim_alerts`, `cis_claim_score_snapshots`, `cis_settings` | Exclusive read/write, managed by GORM AutoMigrate. |

### Why it is enforced, not just documented

`internal/database/migrate.go` holds an explicit allowlist (`ownedModels`) and a
guard that **refuses to start** if a model whose table name lacks the `cis_`
prefix is ever added to it:

```
refusing to migrate table "claims": this database is shared with the AI service
and AutoMigrate may only manage cis_* tables
```

This matters more than it might look. Five AI tables carry pgvector `embedding`
columns of type `vector`, which Go/GORM cannot represent. The Go structs in
`internal/models/ai_tables.go` therefore **omit those fields entirely**. If one
of those models were ever handed to AutoMigrate against a fresh database, GORM
would recreate the table *without* its embeddings and silently break the AI
service's semantic search.

### Verified

Booting the backend against a database preloaded with the AI schema produces:

```
[migrate] migrated 7 backend-owned tables (cis_*); AI-owned tables untouched
```

A column-by-column `information_schema` diff before and after boot — and again
after exercising every F1–F4 endpoint — is **identical**, with all five
`embedding` columns intact and every AI table's row count unchanged.

---

## Backend-owned tables

### `cis_users`
Operator accounts for the login flow. The PRD defines no user model, so this is
minimal and has **no roles**.

| Column | Notes |
|---|---|
| `id` | uuid, generated in Go |
| `email` | unique, lowercased |
| `password_hash` | bcrypt |
| `name`, `last_login_at`, `created_at`, `updated_at` | |

### `cis_refresh_tokens`
Rotating refresh tokens. Only the SHA-256 hash is stored; the raw token is
returned once and never persisted. `revoked_at` supports single-use rotation and
logout-everywhere.

### `cis_policies` — F2
The Public Policy Bank, owned end to end.

| Column | Purpose |
|---|---|
| `name`, `description` | Card and detail page |
| `rolled_out_date` (date) | Drives `status` (US41) |
| `status` | `rolled_out` / `not_rolled_out`, **derived, never user-set** |
| `file_name`, `file_path`, `file_mime_type`, `file_size_bytes` | Uploaded document (US40) |
| `ai_policy_id` (uuid, nullable, **no FK**) | Soft reference to the AI service's `policies.id` |
| `processing_status`, `processing_error`, `processing_attempts`, `processed_at` | Matchmaking state, drives the "Processing" badge (US42) |
| `created_by` | `cis_users.id` |

**`ai_policy_id` is the crux of the design.** Because the backend never inserts
into `policies`, an F2 policy has no identity on the AI side until the AI service
creates one and reports it back via the matchmaking callback. Every
claim↔policy correlation joins through this column:

```sql
-- Existing claims (many-to-many, US12/US39)
claim_policies.policy_id = cis_policies.ai_policy_id
-- Synthetic claims (one-to-many, US20/US39)
claims.policy_id         = cis_policies.ai_policy_id
```

### `cis_claim_reviews` — F1
The human status overlay (US10/US18): `unreviewed`, `active`, `inactive`,
`action_taken`. One row per claim, keyed by `claim_id` (soft reference, no FK).

Reads resolve a claim's status as:

```sql
LEFT JOIN cis_claim_reviews rev ON rev.claim_id = c.id
SELECT COALESCE(rev.status, 'unreviewed') AS review_status
```

The AI service's own `claims.status` is **left untouched**. That means a
pipeline re-run can never silently overwrite a reviewer's decision, and you get
an audit trail (`reviewed_by`, `reviewed_at`, `notes`) for free. Filtering
happens inside the SQL, so pagination over a status tab stays correct.

### `cis_claim_alerts` — F3
The watchlist (US14/US29/US30). One row per watched claim.

`chart_visible` backs the `[C3]` checkbox that decides which claims `[C1]` and
`[C2]` render (US28). Deleting the row therefore also unchecks the claim from
the chart, which is exactly what US14's "Remove" requires — no extra bookkeeping.

Only Existing/Generic claims may be inserted (US26); the service enforces it.

### `cis_claim_score_snapshots` — F3
Point-in-time copies of a claim's Section 6 scores, indexed on
`(claim_id, captured_at)`.

The AI service stores only a claim's *current* score, but US27 plots
`final_claim_score` over time. A cron job copies scores here so the chart has a
history — again, without writing an AI-owned table. Only **watched** claims are
captured, since they are the only ones charted, and snapshotting every claim
hourly would grow without bound. History is pruned after ~400 days.

### `cis_settings` — F4
Key/value global configuration, seeded on first boot and never overwritten
afterwards (`ON CONFLICT DO NOTHING`), so an operator's saved value survives
restarts.

| Key | Type | Purpose |
|---|---|---|
| `alert_threshold` | number | Global Over/Under Threshold cutoff, 0–100 (US32) |
| `claims_last_fetched_at` | timestamp | The S1 "last fetched" label (US9/US33) |

---

## How AI columns are consumed

| PRD concept | AI column | Notes |
|---|---|---|
| R, V, F, H, EI | `claims.reach_score`, `velocity_score`, `falseness_score`, `harm_score`, `emotional_intensity_score` | Read only; clamped to 0–100 on output |
| EI_opposing | `claims.emotional_intensity_opposing` | Display-only, never scored (PRD 6.4.6) |
| Harm sub-scores | `claims.harm_public_safety` etc. | Returned with their published weights |
| ClaimScore / NPR / DiscountFactor / FinalClaimScore | matching columns | Returned together (US23) |
| Dormancy | `claims.is_dormant` | Suppresses NPR + discount (US25) |
| Debunk/Prebunk draft | `claims.activity_content`, `activity_generated_at` | Served from cache; never regenerated on view |
| Positive statements | `content_items.stance = 'supporting'` | US12 |
| Negative statements | `content_items.stance = 'opposing'` | US12; `neutral` excluded, matching PRD 6.4.2 |
| Top 5 Accounts | `content_items` grouped by `author_id`, Supporting side | US12, scope per PRD 6.1.1 |

`claim_type` aliases (`generic`/`existing`, `synthetic`/`predicted`/…) are
normalized in `models.NormalizeClaimType`. An unrecognized value falls back to
`non_existing`, because presenting an unknown claim as unscored is safer than
implying it carries a score.

---

## Local development without the AI service

`docs/sql/00_ai_reference_schema.sql` reproduces the AI team's DDL, including
the pgvector columns, so a blank local Postgres can be bootstrapped.

**It is never executed by the application** and must not be run against the
shared Supabase database — the AI team owns those tables there. See
[SETUP.md](SETUP.md).

If the AI tables are absent, the backend still starts and logs a warning; F2 and
F4 work, while claim and topic endpoints return empty results.
