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
                                 │ 5 outbound HTTP calls
                                 ▼
                          AI service HTTP API
```

**The frontend never talks to the AI service.** Every AI capability an operator
needs must be reachable through a backend endpoint — an AI endpoint with no
backend caller is, from the product's point of view, an endpoint that does not
exist. That single rule is why the flow list below is six long rather than
three.

| # | Flow | Direction | Trigger |
|---|---|---|---|
| 1 | Policy matchmaking (US42) | Backend → AI | A policy is uploaded, rematched, or its document replaced (F2) |
| 2 | Matchmaking result | AI → Backend | The AI job finishes |
| 3 | Generate Generic Claim (US33) | Backend → AI | The F4 test button |
| 4 | Harm confirmation | Backend → AI | An analyst confirms or overrides the harm sub-scores (F1 detail) |
| 5 | Score re-evaluation | Backend → AI | Hourly cron, before the snapshot; or the F4 manual trigger |
| 6 | Sample content generation / clustering | Backend → AI | The F4 "Generate sample data" button |

Everything else is plain database reads. A seventh flow — F5's coordinated
network detection, PRD v1.4 — is not started on either side and is deliberately
out of scope here.

Every outbound call degrades gracefully: with `AI_SERVICE_URL` empty the backend
runs normally, policies record `processing_status: "skipped"`, and every F4
button returns `503` with an explanation rather than failing obscurely.

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
| `debunk_core_fact`, `debunk_nuanced_flag`, `debunk_reiterated_fact` | The same debunk split into the Truth Sandwich's three blocks (Existing claims only) |
| `harm_human_confirmed` | `false` until a reviewer confirms through Flow 4 — never set by the pipeline |

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

**The three `debunk_*` blocks are returned alongside it,** as
`activity.debunk = {core_fact, nuanced_flag, reiterated_fact}`, so the frontend
can render three labelled sections instead of one paragraph. The flat
`activity.content` stays the copyable block. The object is omitted entirely when
all three columns are null.

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
  "document_url": "https://<project>.supabase.co/storage/v1/object/sign/...",
  "force": false,
  "callback_url": "https://cis-backend.internal/api/v1/internal/policies/b15bb20f-.../matchmaking-result"
}
```

`policy_id` is the **`cis_policies.id`**. Echo it back in the callback.

`document_url` is a time-limited signed link (default 1 hour). Fetch the
document rather than expecting bytes over the wire. It is absent if signing
failed; the call still proceeds so you can work from the name alone.

### `force` — please honour this

**This is an ask on the AI service.** Today a repeat submission for a
`policy_id` you already hold short-circuits: the pipeline does not re-run, and
the previously persisted counts are re-reported as `completed`. That is the
right default for a retried webhook, and wrong for the three cases the backend
actually needs:

| Backend action | What the operator asked for | What the short-circuit gives them |
|---|---|---|
| `POST /policies/:id/rematch` after a failure | Run matchmaking again | `completed` with the failed run's counts — usually `0, 0`. A failed matchmaking can never recover. |
| `PUT /policies/:id/file` (replace the document) | Correlations against the **new** document | The new document is never read; correlations stay pinned to the superseded one. |
| The retry sweep | Recover a lost callback | Correct as-is — this is the case the short-circuit is for. |

So the backend now sends `force: true` on rematch and on a document
replacement, and leaves it off for the retry sweep. When `force` is set, please
re-run the pipeline and **supersede** that policy's prior `claim_policies` rows
and predicted claims rather than duplicating them. Until it is honoured the
field is simply ignored — Pydantic drops unknown fields — so nothing breaks
either way; the two operator actions above just keep not working.

### `callback_url` — optional, and worth honouring

Sent only when the backend's `BACKEND_PUBLIC_URL` is configured; omitted
otherwise. Honouring it (falling back to your own `BACKEND_URL` when absent)
makes one AI deployment serve several backend environments, and removes the
highest-consequence unset-by-default variable in the whole integration — see
the note under Configuration.

Path: `pathMatchmaking` in `internal/aiclient/endpoints.go`.

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

Send `Content-Type` and the body, nothing else — this is a machine-to-machine
callback and does not take an operator JWT. See [api/internal.md](api/internal.md).

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

**Retries:** the backend re-queues stuck matchmaking every 15 minutes
(`CRON_MATCHMAKING_RETRY_SPEC`), up to 3 attempts per policy, and an operator
can retry manually with `POST /api/v1/policies/:id/rematch`. Keep the endpoint
idempotent for a given `policy_id` unless `force` is set — do not duplicate
synthetic claims on a retry.

**A lost callback is recovered on the backend side, and only there.** Your
callback is best-effort and never retried, which is fine — but it means that if
it never lands (your `BACKEND_URL` unset, a restart mid-job, a network drop),
the policy sits at `processing_status: "processing"` with a null `ai_policy_id`.
Nothing else moves it out of that state: only the callback does. So the retry
sweep also picks up any policy that has been `processing` for longer than
`AI_MATCHMAKING_STALE_AFTER` (default 30 minutes) and submits it again. That is
the case `force` deliberately stays off for: a run that genuinely succeeded and
only lost its callback should be cheap for you to re-report.

---

## Flow 3 — Generate Generic Claim (US33)

The F4 MVP button. The backend cannot satisfy this itself, since it never writes
`claims`.

```http
POST {AI_SERVICE_URL}/api/v1/claims/generate-generic
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

> **Open ask on the AI service:** everything on that list is produced today
> **except the `claim_policies` link**. The generated claim's "Related Policies"
> panel therefore renders empty — which is precisely one of the panels the demo
> exists to exercise. Either link the generated claim to an existing policy
> (nearest by embedding, or simply the most recent) or accept an optional
> `policy_id` in this request body. If it is deliberately out of scope, say so
> and this paragraph becomes the note explaining why the panel is empty.

**This call is slow by design** — the AI service documents ~30–60s of sequential
LLM calls — so it runs on `AI_SERVICE_LONG_TIMEOUT` (default `180s`), not the
30-second timeout used for the Flow 1 hand-off. Timing it out on the short
budget would report failure to the operator while the AI service kept going and
committed the claim anyway.

Path: `pathGenerateClaim` in `internal/aiclient/endpoints.go`.

---

## Flow 4 — Harm confirmation

An analyst on the F1 detail page confirms the AI's Harm sub-scores, or overrides
some of them. The backend cannot apply this itself: `harm_*`,
`harm_human_confirmed` and every score derived from them are columns on the
AI-owned `claims` table.

```http
PUT  /api/v1/claims/:id/harm/confirm        (backend, from the frontend)
  ↓
PATCH {AI_SERVICE_URL}/api/v1/claims/{id}/harm/confirm
```

```json
{ "public_safety": 90.0, "institutional_trust": null, "economic": null, "policy_disruption": null }
```

Every field is optional and on a 0–100 scale; an omitted one keeps the AI's own
classification. An empty body is the legitimate "I reviewed these and they are
right" case, which still flips `harm_human_confirmed`.

The AI service recomputes `harm_score → claim_score → final_claim_score` and
appends a `claim_score_snapshots` row. The backend ignores the response body and
re-reads the claim from the database, so what it returns to the frontend is the
full `ClaimDetail` built from the same source as every other claim read.

Rejected before the call: a Synthetic claim, which carries no scores at all
(`422`), and an unknown claim (`404`).

Path: `harmConfirmPath()` in `internal/aiclient/endpoints.go` — the one route
with a parameter in it.

---

## Flow 5 — Score re-evaluation

```http
POST {AI_SERVICE_URL}/api/v1/claims/rescore   ->  { "claims_rescored": 4 }
```

**Why this exists at all:** a claim's score moves with wall-clock time even when
nothing is ingested. NPR drifts as opposing posts age out of the rolling window,
which changes the discount factor and therefore `final_claim_score`. But nothing
recomputes that on a schedule — the AI service has no cron of its own, and
clustering only runs when content arrives.

So the backend's hourly snapshot job (`CRON_SCORE_SNAPSHOT_SPEC`) calls this
first and captures afterwards. Without that ordering it would copy the same
number into `cis_claim_score_snapshots` every hour and draw US27's trend chart
as a horizontal line by construction. A failed rescore is logged and the capture
still runs: stale scores beat a gap in the chart.

The same trigger is exposed manually as `POST /api/v1/admin/rescore`.

The backend owns this schedule deliberately: it already runs cron, and the
snapshot must happen *after* the rescore, which is trivially ordered when one
process drives both.

Path: `pathRescore` in `internal/aiclient/endpoints.go`.

---

## Flow 6 — Sample content generation

Until a live crawler exists, the AI service's synthetic ingestion is the **only**
way `content_items` — and therefore Existing claims — come into being. Outside
of Flow 1's predicted claims and Flow 3's single demo claim, nothing else
populates the databank at all.

```http
POST /api/v1/admin/generate-sample-content     (backend, from F4)
  ↓
POST {AI_SERVICE_URL}/api/v1/ingest/generate-synthetic
```

```json
{ "count": 10, "topic_hint": "road pricing", "auto_cluster": true }
```

All three fields are optional; the AI service's own defaults apply (10 items,
auto-clustered). `count` is capped at 50 on both sides. With `auto_cluster`
left on, clustering runs synchronously before the response returns, so this too
uses `AI_SERVICE_LONG_TIMEOUT`.

`POST /api/v1/admin/cluster-now` proxies `POST /api/v1/claims/cluster-now` for
the case where content was ingested but its background clustering pass has not
finished.

**Not proxied, deliberately:** `POST /ingest` and `POST /ingest/batch`. Those
are a machine crawler's interface; a crawler should call the AI service directly
rather than routing content through a human-facing, JWT-authenticated backend.

Paths: `pathGenerateContent` and `pathClusterNow` in
`internal/aiclient/endpoints.go`.

---

## Configuration

**Backend side** (`.env`):

```dotenv
AI_SERVICE_URL=https://ai.internal.yourcity.go.id

# Two timeouts, not one. The short budget covers the Flow 1 hand-off (acked in
# milliseconds) and the readiness probe; the long one covers every call that
# does LLM work inside the request.
AI_SERVICE_TIMEOUT=30s
AI_SERVICE_LONG_TIMEOUT=180s

# How long a policy may sit in processing_status="processing" before the retry
# sweep assumes its Flow 2 callback was lost.
AI_MATCHMAKING_STALE_AFTER=30m

# Optional. When set, sent as `callback_url` on Flow 1.
BACKEND_PUBLIC_URL=
```

The callback route is a machine-to-machine endpoint — see
[api/internal.md](api/internal.md).

### Where the AI service's routes live

Not in the environment. The seven paths the backend calls are constants in
[`internal/aiclient/endpoints.go`](../internal/aiclient/endpoints.go):

| Constant | Path | Flow |
|---|---|---|
| `pathMatchmaking` | `POST /api/v1/matchmaking/policies` | 1 |
| `pathGenerateClaim` | `POST /api/v1/claims/generate-generic` | 3 |
| `harmConfirmPath(id)` | `PATCH /api/v1/claims/{id}/harm/confirm` | 4 |
| `pathRescore` | `POST /api/v1/claims/rescore` | 5 |
| `pathGenerateContent` | `POST /api/v1/ingest/generate-synthetic` | 6 |
| `pathClusterNow` | `POST /api/v1/claims/cluster-now` | 6b |
| `pathHealth` | `GET /health` | — |

They are part of the *contract* between the two services, not properties of a
deployment. A route moving is a code change on both sides — a new request or
response shape almost always comes with it, and an environment variable would
not have saved us from that. Keeping them in the client means the whole outbound
surface is legible in one place, next to the types it belongs to, and a rename
is a reviewable diff rather than a silent difference between two deployments'
`.env` files.

If the AI team moves a route, change the constant and this table in the same
commit.

### Deployment topology

The backend and the AI service talk to each other directly. The two are assumed
to be co-located and reachable only from each other — deploy the
`/api/v1/internal/` callback prefix so it is served only from the AI service,
e.g. restricted at the ingress/load balancer or bound to an internal-only
listener. See [api/internal.md](api/internal.md).

**AI service side:** just the backend's base URL.

> **Deployment checklist, one line:** `BACKEND_URL` on the AI service is the
> single highest-consequence unset-by-default variable in the integration. Unset,
> Flow 2 is skipped entirely with only a log warning, and every policy sits on a
> "Processing" badge until the backend's staleness sweep gives up after three
> attempts. Setting it, or honouring the `callback_url` the backend now sends,
> both fix it.

`/health/ready` reports the AI service as `{configured, reachable}` — a URL
being set says nothing about anything listening on it. An unreachable AI service
never fails readiness: the backend serves F1, F2 and F3 in full without it.

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
  cannot overwrite it and it cannot overwrite yours. In practice `claims.status`
  stays `unreviewed` forever in production, which also makes your own
  `GET /claims/existing?status=` filter meaningless there.
- Run `scripts/seed_demo_data.py` or `scripts/reset_schema.py` against a
  database a backend is pointed at. They correctly leave `cis_*` alone, but
  every backend reference into your tables is a soft one with no foreign key, so
  nothing cascades: reviews, watchlist entries and snapshots are left pointing at
  claims that no longer exist, and policies keep a "completed" badge above empty
  claim lists. `POST /api/v1/admin/reconcile` cleans up after the fact; not
  needing it is better.

### Duplicated state

Three concepts exist in both schemas — the alert threshold
(`admin_settings.over_threshold` vs `cis_settings.alert_threshold`), the
watchlist (`claim_alerts` vs `cis_claim_alerts`), and score history
(`claim_score_snapshots` vs `cis_claim_score_snapshots`). **The backend's copy is
authoritative for everything the frontend sees.** Your copies are correct for
your own admin panel and stale for anything else — `admin_settings` in
particular stays at its 70.0 default forever, diverging the moment an operator
changes the threshold on F4.

Score history is the exception the backend reads both ways: yours is appended on
every rescore for every claim, the backend's is sampled hourly for watched
claims only, and the F3 chart merges the two.

See [DATABASE.md](DATABASE.md) for the full ownership matrix.

---

## Open asks on the AI service

Collected in one place, in priority order.

| # | Ask | Why it matters |
|---|---|---|
| 1 | Honour `force` on Flow 1 (re-run and supersede, instead of short-circuiting) | Without it a failed matchmaking can never recover and replacing a policy document never updates its correlations. The backend already sends it. |
| 2 | Create a `claim_policies` link for the Flow 3 demo claim | US33's "fully populated" requirement; the Related Policies panel is empty without it. |
| 3 | Honour `callback_url` on Flow 1, falling back to `BACKEND_URL` | Lets one AI deployment serve several backend environments, and removes the highest-consequence unset-by-default variable. |

None of the three breaks anything today: unknown request fields are ignored, so
the backend's half of each is already in place and inert until the AI side
catches up.
