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
| **AI service — F5 pipeline** | `detection_run`, `coordinated_network`, `network_account`, `account`, `network_edge`, `network_evidence_post`, `network_burst_bin`, `network_claim_link`, `offtopic_cluster`, `evidence_snapshot` | **SELECT only**, same rule. Their *absence* is tolerated, unlike the tables above: F1–F4 work without them and the F5 endpoints answer `503` until the AI service has created them. The pipeline that writes them is built and matches [sql/01_f5_reference_schema.sql](sql/01_f5_reference_schema.sql) column for column. |
| **AI service — v1.5 optional** | `claim_debunk_segments` | **SELECT only.** Probed at boot (`optionalF6Tables`); absent, the claim detail page falls back to the single `activity_content` draft. Now written by the AI service. |
| **This backend** | `cis_users`, `cis_refresh_tokens`, `cis_policies`, `cis_claim_reviews`, `cis_claim_alerts`, `cis_claim_score_snapshots`, `cis_settings` | Exclusive read/write, managed by GORM AutoMigrate. |
| **This backend — F5** | `cis_network_reviews`, `cis_network_review_log`, `cis_coordination_allowlist`, `cis_common_phrases`, `cis_network_reports`, `cis_export_audit_log`, `cis_detector_settings`, `cis_setting_history` | Same. Every one records a **human decision** or a backend-generated artefact. |

Of the AI-owned tables, the backend actually reads eight: `claims`,
`content_items`, `topics`, `policies`, `claim_policies` and
`claim_score_snapshots` on the hot paths, `claim_debunk_segments` on the claim
detail page, plus `topic_volume_buckets` for diagnostics. `claim_alerts`,
`admin_settings`, `fault_lines` and `official_sources` are listed for
completeness and never queried — the first two because the backend keeps its own
authoritative copies (see **Duplicated state**), the last two because they are
the AI pipeline's own inputs.

Two of `content_items`' columns are probed at boot rather than assumed
(`internal/database/migrate.go`'s `optionalF6Columns`). **`sentiment` is now
written by the AI service** on every ingestion path, so F6's Climate Sentiment
Index computes; rows ingested before it shipped carry `NULL` and still count
toward the denominator. **`city` is not, deliberately** — the AI team is holding
it until a second city is configured — so `city.partitioned` reports `false` and
F4's city selection labels this instance rather than filtering it. See
[AI-INTEGRATION.md](AI-INTEGRATION.md#content_items).

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

**Threshold-crossing state (US71, v1.5).** `last_threshold_status` holds the
Over/Under status recorded at the previous evaluation, `crossed_at` and
`crossed_direction` the last flip. A crossing is a *transition between two
evaluations*, so the prior status has to be stored somewhere:
`final_claim_score` alone only says where a claim is now, never that it just
moved. An empty `last_threshold_status` means "not yet evaluated" and seeds the
baseline without notifying — US71 fires only for a genuine transition, and a
first sighting is not one.

### `cis_alert_acknowledgements` — F3 (v1.5)
One row per user: when they last opened F3. US71 clears the sidebar counter and
the row highlights on opening the page, which is a per-person acknowledgment —
one operator opening F3 must not silently clear a colleague's badge. A crossing
counts as unacknowledged for a user when `crossed_at > acknowledged_at`.

### `cis_claim_harm_edits` — F1 (v1.5)
Append-only audit of human overrides of the four Harm sub-scores (US23),
indexed on `(claim_id, edited_at)`.

The values themselves live on the AI-owned `claims` table, which this backend
never writes; `harm_human_confirmed` is the flag it sets *through* the AI
service, and a boolean carries neither who nor when nor what changed. US23
requires all three. Each row also stores the four sub-scores and the composite
`harm_score` as they were **before** the override, so the AI's original
classification is recoverable from the audit trail alone.

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
| `monitored_city` | string | The single Indonesian city this instance monitors (US65, v1.5). Scopes every F6 metric. |
| `city_timezone` | string | IANA zone for the city-local half of every F5 report footer (PRD 10.8, which requires UTC **and** city-local but never names the city) |

`monitored_city` and `city_timezone` are written together: selecting a city
(`PUT /settings/city`) sets both. Before v1.5 the timezone stood alone, which
allowed an instance to monitor Makassar while stamping its reports in Jakarta
time. The city catalog itself is a closed set in `internal/models/f6_cities.go`,
not a table — reference data that changes on a human timescale, and a table
would be a second source of truth against the zone.

The ~20 detector parameters deliberately do **not** live here. See
`cis_detector_settings` below.

---

## Backend-owned tables — F5

The ownership split for F5 follows one line: **the pipeline's output is
AI-owned, the human's judgement about it is backend-owned.** PRD 10.10 declares
`coordinated_network.review_status` as a column on the pipeline's own table and
`coordination_allowlist` as an unprefixed table; both are moved here instead.
The startup guard would refuse to migrate either — but the stronger reason is
that an analyst's verdict living on a table the pipeline rewrites would be
erased by the next detection run, which is exactly why `cis_claim_reviews`
exists for F1.

### `cis_network_reviews` — F5 review status (US52)
Overlay on `coordinated_network`, one row per reviewed network. Status is
`unreviewed` / `confirmed_coordinated` / `dismissed_false_positive` /
`action_taken` — deliberately **not** the F1 claim status set, which the PRD
keeps separate. A status change requires a reason.

### `cis_network_review_log` — append-only (US52)
Every status change, with `from_status`, `to_status`, the mandatory reason, the
user, the timestamp, and **`signal_profile_json`**: a write-time snapshot of the
network's scores at the moment of the decision.

The snapshot is the point. PRD 10.9.3 requires dismissals to be reviewable in
aggregate so the false-positive rate and its signal profiles can be measured,
and reading those scores back from `coordinated_network` at query time does not
work — a re-run changes them. A column added later leaves every dismissal
recorded before that point permanently unanalysable.

### `cis_coordination_allowlist` — declared coordination (US56, US63)
Accounts the team has declared as legitimately coordinating: NGOs, campaigns,
newsrooms. The one place the read direction between the two services reverses —
the backend owns it and **the pipeline consumes it**, via
`GET /api/v1/internal/detection/exclusions`.

Carries both an addition reason and a **separate removal reason**: US63 requires
a reason for removal and PRD 10.10's single `reason` column can only hold one of
the two.

### `cis_common_phrases` — the phrase allowlist
Slogans, hashtags and civic boilerplate excluded from duplication scoring, so a
shared campaign hashtag is not read as content duplication. Required by the
pipeline spec; PRD 10.10 declares no table for it.

### `cis_network_reports` — generated artefacts (US58, US59, US60)
One row per generated PDF or evidence-bundle ZIP: object path, SHA-256, size,
the sections included, the redaction settings, and the version. Never
overwritten — a report already sent to a platform must stay re-downloadable
exactly as it was sent.

### `cis_export_audit_log` — who exported what (US64)
Report/bundle id, **network id**, **detection run id**, export type, user,
timestamp, sections, redaction settings. The network and run ids are separate
columns rather than PRD 10.10's single generic `object_type`/`object_id` pair,
which can reference only one of the three.

The audit row is written **before** the file is rendered, because PRD 10.8
item 10 prints the audit entry id inside the document. "Log the export after it
succeeds" would produce a report with an empty chain-of-custody slot.

Viewable by any authenticated user, not only admins — there is no role system
anywhere in this backend. The audit property comes from attribution and logging,
not from an access check. See `docs/local_docs/PRD-v1.4.md` 3.3.

### `cis_detector_settings` — the detector control panel (US62)
A **typed single-row table**, not more `cis_settings` keys. ~20 governed
parameters with two cross-field constraints a key/value store cannot express:
the signal weights must sum to 1.00, and the run cadence must be ≤ W/2 so
consecutive detection windows overlap by 50% (PRD 10.5.1) instead of leaving a
boundary blind spot.

Seeded with PRD 10.11's defaults on first boot, `ON CONFLICT DO NOTHING`:
silently resetting a governed parameter to its default would be
indistinguishable from an admin changing it, except that nothing would appear in
the history to say who did.

### `cis_setting_history` — versioned changes (US62)
Every parameter change with its old value, new value, user and timestamp.
Changing a parameter never retroactively alters a stored detection: each
`detection_run` carries the whole parameter set that was in force when it ran.

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

Both scripts now **refuse to run** when they detect any `cis_%` table, unless
explicitly overridden with a flag — so the accident this section describes has to
be committed on purpose. The reconcile sweep below stays, because an override is
still possible and because a reset performed before that guard existed leaves the
same dangling rows.

`POST /api/v1/admin/reconcile` sweeps all four: it deletes the orphaned overlay
rows and re-queues any policy whose AI record vanished, so its correlations can
be rebuilt. It refuses to run when the `claims` table is empty — that usually
means the backend is pointed at the wrong database rather than that the data was
deliberately wiped — unless called with `force: true`. See
[api/settings.md](api/settings.md).
