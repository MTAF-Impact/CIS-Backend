# AI Service Integration

This document is the contract between the CIS backend and the separately
developed AI service. Share it with the AI team.

---

## The model

**The shared Postgres database is the primary integration surface, not an API.**

The AI service owns detection, scoring, and content generation, and writes those
results into its own tables. The backend reads them and adds the human
operational layer — review status, watchlist, thresholds, policy documents —
in its own `cis_*` tables.

```
                      ┌──────────────────────┐
   social media ─────▶│     AI service       │
                      │ detect · cluster ·   │
                      │ score · generate     │
                      └──────────┬───────────┘
                                 │ writes
                                 ▼
                   ┌───────────────────────────────┐
                   │   Supabase Postgres           │
                   │                               │
                   │  AI-owned      backend-owned  │
                   │  claims        cis_users      │
                   │  topics        cis_policies   │
                   │  policies      cis_claim_*    │
                   │  content_items cis_settings   │
                   └───────────┬───────────────────┘
                               │ reads AI tables
                               │ read/writes cis_* tables
                               ▼
                      ┌──────────────────────┐
   frontend ◀────────▶│   This backend       │
                      └──────────┬───────────┘
                                 │ 2 outbound HTTP calls
                                 ▼
                          AI service HTTP API
```

Only **three** touchpoints exist. Everything else is plain database reads.

| # | Flow | Direction | Trigger |
|---|---|---|---|
| 1 | Policy matchmaking (US42) | Backend → AI | A policy is uploaded through F2 |
| 2 | Matchmaking result | AI → Backend | The AI job finishes |
| 3 | Generate Generic Claim (US33) | Backend → AI | The F4 test button |

Both outbound calls degrade gracefully: with `AI_SERVICE_URL` empty, the backend
runs normally, policies record `processing_status: "skipped"`, and the F4 button
returns `503`.

---

## What the AI service must write

### `claims`

| Column | Expectation |
|---|---|
| `claim_type` | See the vocabulary note below |
| `claim_statement`, `topic_id`, `first_caught_at` | Required |
| `reach_score`, `velocity_score`, `falseness_score`, `harm_score`, `emotional_intensity_score` | **0–100 each** (PRD 6.2) |
| `harm_public_safety`, `harm_institutional_trust`, `harm_economic`, `harm_policy_disruption` | 0–100 sub-scores (PRD 6.2.4) |
| `emotional_intensity_opposing` | 0–100, diagnostic only — must **not** be folded into any score (PRD 6.4.6) |
| `claim_score` | `0.15·R + 0.15·V + 0.30·F + 0.30·H + 0.10·EI` (PRD 6.3) |
| `npr` | 0–1 (PRD 6.4.3) |
| `discount_factor` | `1 − (γ × NPR)`, γ = 0.5 ⇒ range 0.5–1 (PRD 6.4.4) |
| `final_claim_score` | `claim_score × discount_factor`, 0–100 — the S1 ranking value |
| `is_dormant` | `true` when supporting + opposing volume is 0 (PRD 6.4.7) |
| `activity_content`, `activity_generated_at` | The Debunk/Prebunk draft, generated **once** |

The backend reads these and never recomputes or writes them. It clamps values
defensively on output, as PRD 6.3/6.4.4 instruct, but does not correct them —
send values already in range.

**Dormancy:** when `is_dormant` is `true`, the backend suppresses `npr` and
`discount_factor` and returns them as `null`, per US25. A dormant claim must be
*flagged*, never discounted.

**Synthetic claims** are unscored: leave the score columns `NULL`. The API omits
them from the response entirely rather than sending zeros.

**`activity_content` must be generated once and cached.** US12/US20 require the
backend to serve it without re-calling the AI on every view, and it does — it
only ever reads the column.

### `content_items`

| Column | Used for |
|---|---|
| `stance` | **`supporting` / `opposing` / `neutral`** — drives both the NPR formula and the US12 Positive/Negative statement lists |
| `author_id` | Top 5 Accounts (US12). A handle like `@driver_jkt` is ideal. |
| `impressions` | Ranks Top 5 Accounts |
| `text`, `source`, `claim_id`, `created_at` | Statement lists |

`stance` is the single most load-bearing field here. The PRD's "Positive
Statements" and "Negative Statements" map to `supporting` and `opposing`;
`neutral` is excluded from both, mirroring PRD 6.4.2 so the card counts always
agree with the score. **Rows with a `NULL` stance appear in neither list and are
excluded from Top 5 Accounts.**

### `claim_type` vocabulary

The backend normalizes aliases, so any of these work:

| Canonical | Accepted values |
|---|---|
| `existing` (Generic) | `existing`, `generic`, `existing_claim`, `generic_claim` |
| `non_existing` (Synthetic) | `non_existing`, `non-existing`, `synthetic`, `predicted`, `synthetic_claim`, `non_existing_claim` |

An unrecognized value falls back to `non_existing` — presenting an unknown claim
as unscored is safer than implying it carries a score. Pick one value and stay
consistent; tell the backend team if you need a new alias
(`internal/models/ai_tables.go`).

---

## Flow 1 — Policy matchmaking (US42)

When a user uploads a policy through F2, the backend:

1. Stores the document and creates a `cis_policies` row with
   `processing_status: "pending"`
2. Returns `201` immediately — the UI shows a "Processing" badge
3. In the background, POSTs to the AI service
4. Waits for the callback

### Request the AI service receives

```http
POST {AI_SERVICE_URL}/api/v1/matchmaking/policies
Content-Type: application/json
X-API-Key: {AI_SERVICE_API_KEY}
Authorization: Bearer {AI_SERVICE_API_KEY}
```

```json
{
  "policy_id": "b15bb20f-947f-479a-b6c1-38aa3a4bdfd0",
  "name": "Jakarta Congestion Charge 2026",
  "description": null,
  "rolled_out_date": "2026-01-15",
  "status": "rolled_out",
  "file_name": "congestion-charge.pdf",
  "file_mime_type": "application/pdf",
  "document_url": "https://<project>.supabase.co/storage/v1/object/sign/..."
}
```

`policy_id` is the **`cis_policies.id`**. Echo it back in the callback.

`document_url` is a time-limited signed link (default 1 hour). Fetch the
document rather than expecting bytes over the wire. It is absent if signing
failed; the call still proceeds so you can work from the name alone.

Path configurable via `AI_SERVICE_MATCHMAKING_PATH`.

### Expected response

Either acknowledge and call back later (recommended for a slow job):

```json
{ "status": "processing" }
```

Or return the result inline:

```json
{
  "ai_policy_id": "b0000000-0000-0000-0000-000000000001",
  "status": "completed",
  "matched_claim_count": 3,
  "generated_claim_count": 2,
  "message": "matched 3 existing claims, generated 2 synthetic claims"
}
```

An empty `202` body is also valid and treated as "accepted, will call back".

### What the AI service does

Per US42:

1. **Match** the policy against Existing/Generic claims already in the databank
   and insert rows into `claim_policies` (many-to-many).
2. **Generate** one or more Non-Existing/Synthetic claims for aspects with no
   existing match, setting `claims.policy_id` (one-to-many).
3. **Assign a topic** to each generated claim — an existing `topics` row, or a
   new one if none fit.
4. Report back (Flow 2).

All of this is written directly to AI-owned tables. The backend does not
mediate it.

### The `ai_policy_id` requirement — read this

The backend **never inserts into the `policies` table**. So an F2 policy has no
identity on your side until you create one.

The AI service must therefore:

1. Create its own `policies` row for the policy (using any id it likes)
2. Link claims to **that** id
3. Return that id as `ai_policy_id`

The backend stores it on `cis_policies.ai_policy_id` and joins every correlation
through it:

```sql
claim_policies.policy_id = cis_policies.ai_policy_id  -- Existing claims
claims.policy_id         = cis_policies.ai_policy_id  -- Synthetic claims
```

**Without `ai_policy_id`, `GET /api/v1/policies/:id` returns empty claim lists
and the card never clears its "Processing" badge**, however many rows you wrote.

---

## Flow 2 — Reporting the result

```http
POST {BACKEND_URL}/api/v1/internal/policies/{policy_id}/matchmaking-result
Content-Type: application/json
```

No `X-Internal-Key` header is required in this deployment (`INTERNAL_API_KEY`
is unset on the backend). Only add the header if `INTERNAL_API_KEY` gets
configured on both sides later.

```json
{
  "ai_policy_id": "b0000000-0000-0000-0000-000000000001",
  "status": "completed",
  "matched_claim_count": 3,
  "generated_claim_count": 2
}
```

On failure:

```json
{ "status": "failed", "error": "could not extract text from the PDF" }
```

Full reference: [api/internal.md](api/internal.md).

You may send `ai_policy_id` with `status: "processing"` to let correlations
resolve early, then follow up with `completed`.

**Retries:** the backend retries a failed matchmaking up to 3 times via a daily
job, and an operator can retry manually with
`POST /api/v1/policies/:id/rematch`. Make the endpoint idempotent for a given
`policy_id` — do not duplicate synthetic claims on a retry.

---

## Flow 3 — Generate Generic Claim (US33)

The F4 MVP button. The backend cannot satisfy this itself, since it never writes
`claims`.

```http
POST {AI_SERVICE_URL}/api/v1/claims/generate-generic
X-API-Key: {AI_SERVICE_API_KEY}
```

```json
{ "claim_type": "existing", "topic_id": "a0000000-0000-0000-0000-000000000001" }
```

`topic_id` is optional. Response:

```json
{
  "claim_id": "9f2c...",
  "claim_statement": "The new emissions rule will shut down all delivery vans",
  "topic_id": "a0000000-0000-0000-0000-000000000001",
  "message": "generated"
}
```

US33 requires the inserted claim to be **fully populated** — every field the S1
card (US10) and detail page (US12) need: statement, complete score breakdown,
topic, `first_caught_at`, positive/negative `content_items` with stances, a
`content_items` set with `author_id` values for Top 5 Accounts, a `claim_policies`
link, and a cached `activity_content` debunk draft.

Path configurable via `AI_SERVICE_GENERATE_CLAIM_PATH`.

---

## Configuration

**Backend side** (`.env`):

```dotenv
AI_SERVICE_URL=https://ai.internal.yourcity.go.id
AI_SERVICE_API_KEY=
AI_SERVICE_TIMEOUT=30s
AI_SERVICE_MATCHMAKING_PATH=/api/v1/matchmaking/policies
AI_SERVICE_GENERATE_CLAIM_PATH=/api/v1/claims/generate-generic

INTERNAL_API_KEY=
```

Both are optional shared secrets, one per direction, and both are unset in
this deployment — the backend and AI service are assumed to only be reachable
from each other over a private network, so no key is exchanged either way.
`AI_SERVICE_API_KEY` empty means outbound calls carry no auth header;
`INTERNAL_API_KEY` empty means the callback route accepts requests with no
`X-Internal-Key` header (see [api/internal.md](api/internal.md)). Set either
one on both sides later if the network boundary ever needs it.

**AI service side:** just the backend's base URL — no key to configure unless
`INTERNAL_API_KEY` is set above.

---

## Boundaries

Things the backend will **never** do, by design:

- Insert, update, or delete any row in `claims`, `topics`, `policies`,
  `claim_policies`, `content_items`, or any other AI-owned table
- Run `ALTER TABLE` / AutoMigrate against them — a startup guard refuses to
  migrate any table not prefixed `cis_`
- Read or write the pgvector `embedding` columns; they are absent from the Go
  models entirely
- Recompute a Section 6 score
- Call the AI service when a user merely *views* a claim

Things the AI service should **never** do:

- Write any `cis_*` table. Use the callback endpoint instead.
- Assume `claims.status` reflects a human decision — it does not. Reviewer
  status lives in `cis_claim_reviews`, deliberately, so your pipeline re-runs
  cannot overwrite it and it cannot overwrite yours.

See [DATABASE.md](DATABASE.md) for the full ownership matrix.
