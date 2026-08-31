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
| **AI service** | `claims`, `content_items`, `topics`, `policies`, `claim_policies`, `topic_volume_buckets`, `claim_alerts`, `claim_score_snapshots`, `admin_settings`, `fault_lines`, `official_sources` | **SELECT only.** Never inserted, updated, deleted, or migrated. |
| **This backend** | `cis_users`, `cis_refresh_tokens`, `cis_policies`, `cis_claim_reviews`, `cis_claim_alerts`, `cis_claim_score_snapshots`, `cis_settings` | Exclusive read/write, managed by GORM AutoMigrate. |

Of the AI-owned tables, the backend actually reads eight: `claims`,
`content_items`, `topics`, `policies`, `claim_policies` and
`claim_score_snapshots` on the hot paths, plus `topic_volume_buckets` for
diagnostics. `claim_alerts`, `admin_settings`, `fault_lines` and
`official_sources` are listed for completeness and never queried — the first two
because the backend keeps its own authoritative copies (see **Duplicated
state**), the last two because they are the AI pipeline's own inputs.

> Corrected on 2026-08-31: this matrix previously listed `narratives` and
> `intervention_responses`, which the AI service no longer has, and omitted
> `claim_alerts`, `claim_score_snapshots` and `admin_settings`, which it does.
> The two dead models were also deleted from `internal/models/ai_tables.go`;
> `AIInterventionResponse` in particular looked like the home of the
> `core_fact` / `nuanced_flag` / `reiterated_fact` Truth Sandwich fields, which
> actually live on `claims.debunk_*`.

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

## Duplicated state

Three concepts exist twice, once per service. **The backend's copy is
authoritative in every case** — it is the only one the frontend ever sees.

| Concept | AI table | Backend table | Authority |
|---|---|---|---|
| Alert threshold | `admin_settings.over_threshold` (default 70) | `cis_settings.alert_threshold` (default 70) | **Backend.** The AI copy stays at 70 forever and silently diverges the moment an operator changes the threshold on F4. |
| Watchlist | `claim_alerts` | `cis_claim_alerts` | **Backend.** The AI copy stays empty. |
| Score history | `claim_score_snapshots` | `cis_claim_score_snapshots` | **Backend writes only its own** — but it *reads* both. See below. |

This is not a bug today, because nothing reads the AI's copies for a
frontend-facing decision. It is written down because it is easy to mistake one
for the other later: anyone querying the AI service's `GET /alerts` or
`GET /admin/settings` — its standalone admin panel, or a frontend shortcut that
skips the backend — gets stale, wrong data. Deleting the AI-side tables is not
necessary; its own admin panel legitimately uses them.

**Score history is the one place the backend reads the AI's copy.** The two
tables have complementary shapes:

| | `cis_claim_score_snapshots` | `claim_score_snapshots` (AI) |
|---|---|---|
| Written | Hourly, by cron | On every rescore, by the AI service |
| Covers | Watched claims only | Every claim it touches |
| Columns | The full score breakdown | `final_claim_score` only |

`GET /claims/:id/score-history` is offered on every claim, but the backend's own
table is empty for any claim that was never bell-icon'd. So
`SnapshotRepository.Series` reads both and merges them per time bucket, summing
before averaging so a bucket with three backend rows and one AI row weights them
equally. The AI read is best-effort: if that table is missing, the query falls
back to backend snapshots alone rather than failing.

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
| Truth Sandwich blocks | `claims.debunk_core_fact`, `debunk_nuanced_flag`, `debunk_reiterated_fact` | The same debunk pre-split into three labelled blocks; returned as `activity.debunk` |
| Harm confirmation | `claims.harm_human_confirmed` | Read on the detail page, set through `PUT /claims/:id/harm/confirm`, which proxies to the AI service — the backend never writes the column itself |
| Score history | `claim_score_snapshots.final_claim_score`, `recorded_at` | Merged with `cis_claim_score_snapshots` for the F3 chart |
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

---

## When the AI side is reset

The AI service's `scripts/seed_demo_data.py` and `scripts/reset_schema.py` both
correctly leave `cis_*` alone — but every backend reference into an AI table is
a **soft** one, with no foreign key, precisely because the backend must never
constrain a table it does not own. So nothing cascades:

- `cis_claim_reviews.claim_id` → dangling
- `cis_claim_alerts.claim_id` → dangling; F3 lists claims that no longer exist
- `cis_claim_score_snapshots.claim_id` → dangling
- `cis_policies.ai_policy_id` → points at a deleted `policies.id`, so F2 shows a
  "completed" badge above empty claim lists

Nothing errors. The UI just quietly shows wrong things.
`POST /api/v1/admin/reconcile` sweeps all four: it deletes the orphaned overlay
rows and re-queues any policy whose AI record vanished, so its correlations can
be rebuilt. It refuses to run when the `claims` table is empty — that usually
means the backend is pointed at the wrong database rather than that the data was
deliberately wiped — unless called with `force: true`. See
[api/settings.md](api/settings.md).
