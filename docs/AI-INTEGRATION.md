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
                                 │ 8 outbound HTTP calls
                                 ▼
                          AI service HTTP API
```

**The frontend never talks to the AI service.** Every AI capability an operator
needs must be reachable through a backend endpoint — an AI endpoint with no
backend caller is, from the product's point of view, an endpoint that does not
exist. That single rule is why the flow list below is eight long rather than
three.

| # | Flow | Direction | Trigger |
|---|---|---|---|
| 1 | Policy matchmaking (US42) | Backend → AI | A policy is uploaded, rematched, or its document replaced (F2) |
| 2 | Matchmaking result | AI → Backend | The AI job finishes |
| 3 | Generate Generic Claim (US33) | Backend → AI | The F4 test button |
| 4 | Harm confirmation | Backend → AI | An analyst confirms or overrides the harm sub-scores (F1 detail) |
| 5 | Score re-evaluation | Backend → AI | Hourly cron, before the snapshot; or the F4 manual trigger |
| 6 | Sample content generation / clustering | Backend → AI | The F4 "Generate sample data" button |
| 7 | **Detection run (F5)** | Backend → AI | The detection tick, a velocity spike, or an analyst's "run detection" |
| 8 | **Evidence snapshot purge (F5)** | Backend → AI | The nightly retention sweep |
| — | **Exclusion lists (F5)** | AI → Backend | *Optional fallback.* The exclusions already travel inline on Flow 7's body; the pipeline pulls this only if it would rather fetch them |

Everything else is plain database reads.

**All eight flows are now implemented on both sides.** Flows 7 and 8 landed on
the AI side against this document's contract verbatim — `POST
/api/v1/detection/runs` and `POST /api/v1/detection/snapshots/purge`, the ten
tables in [sql/01_f5_reference_schema.sql](sql/01_f5_reference_schema.sql), the
parameters persisted per run, and the exclusions read from the request body
rather than from `cis_coordination_allowlist`. That last point closed the one
place the read direction between the two services would have reversed: the
pipeline never reads a `cis_*` table, so the ownership rule now holds with zero
exceptions in either direction. See Flow 7 for what is still degraded, which is
a *data* gap, not a missing flow.

The `503` degradation described on [api/networks.md](api/networks.md) is
therefore a cold-start state, not a steady one: the F5 tables are created by the
AI service's own schema management, and F5 endpoints answer `503` only until
that has run against the shared database.

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

### `claim_debunk_segments` — new in PRD v1.5 (US12)

v1.5 replaces the single generic Debunk draft with **one tailored copy
recommendation per audience segment** affected by the claim. The generation and
caching rules are unchanged — once, at claim creation, never on view — but the
relation is now one-to-many, so it cannot be a column on `claims`.

**Shipped on the AI side**, against the DDL below unchanged. The pipeline writes
1–4 segments per Existing claim, inferred from the claim's supporting-side
sample, ranked most-exposed first, generated in the same call site and under the
same once-only guard as `activity_content` — and Flow 3's demo claim populates
them too, since it runs through the same construction path. A generation failure
leaves the set empty without invalidating `activity_content`, which is exactly
the fallback this section already assumed.

| Column | Expectation |
|---|---|
| `claim_id` | The Existing claim. Synthetic claims stay unsegmented. |
| `segment_name` | The label shown on the card ("Commuters"). Required — an unlabelled variant reads as the generic draft v1.5 removes. |
| `segment_rationale` | Why this segment was identified; rendered as the card subtitle. Optional. |
| `content` | The copy written for that segment |
| `rank` | Card order, most-exposed segment first |

DDL in [sql/02_f6_reference_schema.sql](sql/02_f6_reference_schema.sql). The
table is **optional**: without it the claim detail page falls back to
`activity_content` exactly as in v1.4, so shipping it is not a hard dependency
for the rest of the product.

### `content_items`

| Column | Used for |
|---|---|
| `stance` | **`supporting` / `opposing` / `neutral`** — drives both the NPR formula and the US12 Positive/Negative statement lists |
| `sentiment` | **New in v1.5 · shipped.** `positive` / `negative` / `neutral` — the Baseline Climate Sentiment half of the F6 Climate Sentiment Index (PRD 6.6.1) |
| `city` | **New in v1.5 · deferred by the AI team.** The resolved city name, scoping every F6 metric to the city configured in F4 (US65) |
| `author_id` | Top 5 Accounts (US12). A handle like `@driver_jkt` is ideal. |
| `impressions` | Ranks Top 5 Accounts |
| `text`, `source`, `claim_id`, `created_at` | Statement lists |

**`sentiment` is not `stance`, and stance cannot stand in for it.** Stance is a
position relative to a specific claim and only exists for content the pipeline
clustered. Opposing a false claim is *good* for the city's information health,
so reading stance as sentiment would invert the index for exactly the content
the product most wants to see. PRD 6.6.1 is also explicit that
`TotalClimateConversationVolume` is "independent of the claim repository" —
unclustered rows count too, and a `NULL` sentiment still counts toward the
denominator so a classification backlog cannot make the city look calmer than it
is.

**`city` must be a normalised city name**, matching the catalog in
`internal/models/f6_cities.go` exactly (`Jakarta`, `Makassar`, …). `location` is
free text written by the source platform and cannot be joined on; the backend
must not be in the business of geocoding it.

Both columns are **optional**: without `sentiment`, F6's O1 gauge reports
`unavailable` and the rest of the page still works; without `city`, the F4
selection labels the instance instead of partitioning it and the API says so.

**Where each one actually stands.** `sentiment` is written on every ingestion
path — single, batch and synthetic — assessed independently of `stance` exactly
as this section asks, so O1's gauge computes. Rows ingested before it shipped
carry `NULL`, which still counts toward the denominator as PRD 6.6.1 requires.
`city` is **deliberately not implemented**: the AI team is holding it until a
second city is actually configured, on the grounds that partitioning on a column
that would only ever hold `Jakarta` buys nothing. So `city.partitioned` stays
`false` and the F4 selection labels this instance — the documented degradation,
now the expected steady state rather than a gap waiting to close.

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

### `force` — honoured

**This was an ask on the AI service; it is now implemented.** The backend sends
`force: true` on rematch and on a document replacement, and leaves it off for
the retry sweep. Three cases, and what each now gets:

| Backend action | What the operator asked for | What happens |
|---|---|---|
| `POST /policies/:id/rematch` after a failure | Run matchmaking again | Re-runs. A failed matchmaking recovers. |
| `PUT /policies/:id/file` (replace the document) | Correlations against the **new** document | Re-runs against the new document; the prior `claim_policies` rows and predicted claim are **superseded**, not duplicated. |
| The retry sweep | Recover a lost callback | Short-circuits and re-reports the persisted counts, which is the whole point of leaving `force` off. |

The AI service keys all of this on `policies.backend_policy_id`, so `Policy.id`
— and therefore `ai_policy_id` — never changes across a re-run, and there is
never a second `Policy` row for one `cis_policies.id`.

**One behaviour beyond what the backend asked for, worth knowing:** the AI
service also re-runs *without* `force` when the previous run failed, tracked on
a nullable `policies.last_matchmaking_error`. `NULL` means "never run, or the
last run succeeded" and is the only state that short-circuits. So the retry
sweep recovers a genuinely failed run on its own, cheaply, without the backend
having to distinguish "lost callback" from "failed job" before deciding whether
to set the flag.

### `callback_url` — honoured

Sent only when the backend's `BACKEND_PUBLIC_URL` is configured; omitted
otherwise. **The AI service now uses it as the full callback target as-is when
present, and falls back to its own `BACKEND_URL` when absent** — so one AI
deployment can serve staging and production without either one hardcoded on its
side, and `BACKEND_URL` stops being the highest-consequence unset-by-default
variable in the integration. Setting `BACKEND_PUBLIC_URL` here is now the
simpler half of that fix; see the note under Configuration.

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

> **Closed.** The `claim_policies` link was the one item on that list the AI
> service did not produce, leaving the "Related Policies" panel empty on exactly
> the claim the demo exists to exercise. It now links the generated claim to the
> nearest `Policy` by embedding cosine similarity, falling back to the most
> recently created one, and no-ops only when no `Policy` exists at all. No
> `policy_id` field was added to this request body — none is needed.

The claim is built by the **same** construction and scoring pipeline real
clustering uses, not a parallel "fake claim" path, so a demo claim carries a
genuine score breakdown, stances, Top 5 Accounts, a cached debunk draft and (as
of v1.5) its `claim_debunk_segments` rows.

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

## Flow 7 — Detection run (F5, PRD 10.5.8)

**This flow is a hand-off, not a computation.** PRD 10.5.8 sets the performance
target at "a 5,000-account, 7-day run completes in under 10 minutes on commodity
hardware" — the worst case the system is allowed to accept, and far beyond any
HTTP request the backend should hold open. So the call announces the run, the AI
service acknowledges in milliseconds, and the work happens in its background.
Same shape as Flow 1, same reason. `AI_SERVICE_TIMEOUT` (the short budget)
applies: a slow response here means the hand-off failed, not that detection is
slow.

```http
POST /api/v1/admin/detection-runs      (backend, on demand)
  or the hourly detection tick / velocity trigger
  ↓
POST {AI_SERVICE_URL}/api/v1/detection/runs
```

```json
{
  "claim_ids": ["c0000000-..."],
  "trigger_source": "scheduled",
  "window_start": "2026-08-24T00:00:00Z",
  "window_end": "2026-08-31T00:00:00Z",
  "parameters": { "window_days": 7, "beta_time": 0.30, "...": "the full detector configuration" },
  "exclusions": { "accounts": [], "phrases": [] }
}
```

**Expected response** — acknowledgement only:

```json
{ "run_id": "...", "status": "pending" }
```

The backend stores nothing from this response beyond echoing the id back to the
caller. `detection_run.status` is the source of truth, it is a row both services
can see, and duplicating it would create a second answer. **The backend never
polls** — it reads the detection tables.

Three details that are not arbitrary:

- **The window is computed by the backend**, not by the AI service. PRD 10.5.1
  requires consecutive runs to overlap by 50% of `W`, and that rule is enforced
  by a cross-field validation on the settings (`cadence <= W/2`). Computing the
  window on the far side of the hand-off would put the guarantee out of reach of
  its own check.
- **The parameters are sent, not read.** US62 requires that changing a parameter
  never retroactively alters a stored detection. Sending them with the request is
  what makes that true even if an admin edits the configuration while a run is in
  flight. Please persist them to `detection_run.parameters_json` verbatim.
- **The exclusions travel with the request.** The declared-coordination allowlist
  and the common-phrase list are backend-owned and pipeline-read. Sending them
  inline is what keeps the ownership rule exception-free: the pipeline reads them
  off the request body and never touches `cis_coordination_allowlist`, so there
  is no AI → `cis_*` read anywhere in the integration. They remain fetchable at
  `GET /api/v1/internal/detection/exclusions`, but that is a fallback nothing
  currently calls.

**A scope rule the backend already enforces, so the pipeline does not have to
rediscover it:** detection never runs over Non-Existing/Synthetic claims
(PRD 10.3). A predicted claim has no real posts, so there is nothing to cluster.
The on-demand endpoint rejects one with `422`, and the scheduled sweep filters on
claim type as well as on status Active.

What the pipeline must write is `docs/sql/01_f5_reference_schema.sql`. Columns
marked **`BEYOND 10.10`** there were the backend's proposal for requirements
Section 10 states but 10.10 declares no column for.

### Where Flow 7 actually stands

**Implemented, against this contract verbatim.** The AI service pulled and
reviewed this repo's merged code (`internal/aiclient/detection.go`,
`internal/models/f5_ai_tables.go`, `docs/sql/01_f5_reference_schema.sql`) rather
than working from a summary of it, and rebuilt its side to match: both endpoint
paths, all ten tables with singular names and table-specific primary keys, the
`BEYOND 10.10` columns **adopted as proposed** (that sign-off is done), and
`detection_run.parameters` persisted verbatim per run so US62 holds.

Three specifics the backend can rely on:

- The `202 {"run_id", "status": "pending"}` ack is real: the `detection_run` row
  is written **synchronously** before the response, so the id is immediately
  queryable. The backend was already right not to poll.
- A `claim_id` that does not resolve to an Existing claim is skipped inside its
  own iteration. One bad id never fails the whole run.
- A cluster that is ≥60% allowlisted is persisted with
  `allowlist_suppressed = true` rather than dropped, so the audit trail stays
  stable as the allowlist changes underneath it. Suppressing it on every surface
  is the backend's to do, from a row that does not move.

**What is degraded, and why it is a data gap rather than a missing flow.** The
detection maths is complete; three of the five signal families have no inputs in
today's `content_items`:

| Family | State | Cause |
|---|---|---|
| `w_time` Synchrony | Computes, on the wrong clock | Runs on `content_items.created_at` — ingest time, not publish time. Ask #4 below. |
| `w_amp` Co-amplification | Effectively always empty | No reshare / quote / reply / outbound-link fields exist. |
| `w_meta` Provenance | Partial | `account.created_at_platform` and `profile_hash` are populated; `bio`, `declared_location` and `client_app` are columns nothing writes. |
| `w_struct` Structural overlap | Always unavailable | No follower-graph source exists. |

Each degrades honestly — reported as unavailable on
`detection_run.signals_unavailable`, never faked as measured. The consequence is
the one this document predicted: **two or more unavailable families cap every
detected network at Medium confidence regardless of score**, so `high` is
currently unreachable in production. F5's list, detail, review, report and audit
surfaces all work; the recall behind them is known-incomplete.

Four smaller gaps the backend renders around, all documented rather than silent:
`network_evidence_post.shared_span_start`/`_end` are always `NULL` (no text-diff
step yet, so no span highlighting), `coordinated_network.relabelled` is never set
(neither side can write it under "no shared-table writes" — open question),
`network_edge`'s per-signal weight columns are `NOT NULL DEFAULT 0` so an
unavailable family reads as `0.0` per edge (`detection_run.signals_unavailable`
is the run-level source of truth — also an open question), and comparison-role
`network_account` rows carry no `layout_x`/`layout_y`.

## Flow 8 — Evidence snapshot purge (F5, PRD 10.9.1 rule 7)

```http
POST {AI_SERVICE_URL}/api/v1/detection/snapshots/purge
{ "network_ids": ["..."] }
  ↓
{ "snapshots_purged": 12 }
```

**The list is computed by the backend, and this can never be a TTL on the AI
side.** Snapshots are retained for a configurable period (default 24 months) and
then purged — *except* where a report was generated from them, in which case the
snapshot lives as long as the report. That exception depends on
`cis_network_reports`, a backend-owned table the pipeline cannot see. A blanket
TTL would eventually purge the evidence under a report already submitted to a
platform, and a report whose evidence has been purged is worthless as evidence.

So the backend selects the eligible snapshots and hands over the list. The rows
are AI-owned, so the deletion itself is the pipeline's to perform.

**Implemented as specified.** An earlier AI-side design had made this an age-only
TTL, which would have dropped the "except reported" carve-out exactly as this
section warned; the network-id-driven shape restored it. The deletion covers the
evidence artefacts — posts, burst bins, edges, membership — and the
`evidence_snapshot` row. `coordinated_network` itself is kept permanently, so a
purged network still lists, still shows its scores, and still carries its review
history; what is gone is the evidence behind it.

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

Not in the environment. The nine paths the backend calls are constants in
[`internal/aiclient/endpoints.go`](../internal/aiclient/endpoints.go), except
F5's two, which live next to the client that calls them in
[`internal/aiclient/detection.go`](../internal/aiclient/detection.go):

| Constant | Path | Flow |
|---|---|---|
| `pathMatchmaking` | `POST /api/v1/matchmaking/policies` | 1 |
| `pathGenerateClaim` | `POST /api/v1/claims/generate-generic` | 3 |
| `harmConfirmPath(id)` | `PATCH /api/v1/claims/{id}/harm/confirm` | 4 |
| `pathRescore` | `POST /api/v1/claims/rescore` | 5 |
| `pathGenerateContent` | `POST /api/v1/ingest/generate-synthetic` | 6 |
| `pathClusterNow` | `POST /api/v1/claims/cluster-now` | 6b |
| `pathDetectionRun` | `POST /api/v1/detection/runs` | 7 |
| `pathDetectionPurge` | `POST /api/v1/detection/snapshots/purge` | 8 |
| `pathHealth` | `GET /health` | — |

All nine are confirmed against the AI service's own `API_REFERENCE.md` and
`GO_INTEGRATION.md`, which number the flows identically — a flow number means the
same thing in either repo.

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

> **Deployment checklist, one line:** *either* `BACKEND_URL` on the AI service
> *or* `BACKEND_PUBLIC_URL` here must be set, and neither is by default. With
> neither, Flow 2 is skipped entirely with only a log warning, and every policy
> sits on a "Processing" badge until the backend's staleness sweep gives up after
> three attempts. The AI service now honours the `callback_url` this backend
> sends and prefers it over its own `BACKEND_URL`, so setting `BACKEND_PUBLIC_URL`
> here is the half of the fix that is under this team's control.

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
  needing it is better. **Both scripts now refuse to run** when they find any
  `cis_%` table, unless explicitly overridden — so this is enforced on the AI
  side rather than left to discipline.

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

## Asks on the AI service — current status

The original ten asks, with where each one now stands. Eight are closed; the two
that remain are the F5 data gap, which was always the hard one.

| # | Ask | Status |
|---|---|---|
| 1 | Honour `force` on Flow 1 (re-run and supersede, instead of short-circuiting) | ✅ **Done.** Plus an automatic re-run after a recorded failure even without `force`. |
| 2 | Create a `claim_policies` link for the Flow 3 demo claim | ✅ **Done.** Nearest policy by embedding, falling back to the most recent. |
| 3 | Honour `callback_url` on Flow 1, falling back to `BACKEND_URL` | ✅ **Done.** Set `BACKEND_PUBLIC_URL` here to use it. |
| 4 | **`content_items.posted_at`** — when the account published, distinct from when we captured it | ❌ **Still open.** Synchrony now computes, but on ingest time, so it measures our crawler's schedule as much as the accounts' behaviour. |
| 5 | **An `account` table** — durable platform account identity, with `created_at_platform`, `profile_hash`, bio, declared location and client/app string | ⚠️ **Partial.** The table exists and the allowlist keys on it. `created_at_platform` and `profile_hash` are populated; `bio`, `declared_location` and `client_app` are columns nothing writes, and `content_items.author_id` is still a bare varchar rather than an FK. |
| 6 | Sign off (or amend) the `BEYOND 10.10` columns in `docs/sql/01_f5_reference_schema.sql` | ✅ **Signed off**, adopted as proposed, column for column. |
| 7 | Implement Flow 7 and Flow 8 | ✅ **Done.** Both endpoints, all ten tables, parameters persisted per run. |
| 8 | **`content_items.sentiment`** (v1.5) | ✅ **Done.** Written on every ingestion path, assessed independently of `stance`. |
| 9 | **`claim_debunk_segments`** (v1.5) | ✅ **Done.** 1–4 segments per Existing claim, Flow 3's demo claim included. |
| 10 | **`content_items.city`** (v1.5) | 🚫 **Deferred, deliberately.** Held until a second city is configured. `city.partitioned` stays `false`; F4's selection labels this instance. |

Asks 8–10 were the PRD v1.5 additions, with DDL in
[sql/02_f6_reference_schema.sql](sql/02_f6_reference_schema.sql). Two shipped
against that DDL unchanged; the third is a product decision rather than an
outstanding request, so the backend's documented degradation for it is now the
expected steady state.

**Asks 4 and 5 are what is left, and they are still the F5 quality ceiling —
though no longer a blocker.** F5 produces data now: the pipeline runs, the
tables fill, and every downstream surface works against real output. What the
missing schema costs is *recall and confidence*, not existence. Two or more
unavailable signal families cap every network at Medium, so a `high`-confidence
network cannot occur in production today, and Synchrony — the signal the PRD's
own worked example leads with — is measured against the wrong clock. Closing
these two is what turns F5 from working into trustworthy. See
`docs/local_docs/PRD-v1.4.md` Section 5 for the signal-by-signal analysis, and
Flow 7 above for the per-family breakdown.

### Still open in the other direction

Two questions the AI team has asked back, both about columns neither side can
currently write under "no shared-table writes":

- **`coordinated_network.relabelled`** — nothing sets it. The AI service writes
  the row and cannot see a later human relabel; the backend can see one and
  cannot write the column. It needs either a flow or a backend-owned overlay
  column, the way `cis_claim_reviews` already handles claim review status.
- **`network_edge`'s per-signal weight columns** are `NOT NULL DEFAULT 0`, so an
  unavailable family is indistinguishable from a measured zero at edge level.
  `detection_run.signals_unavailable` is the run-level answer; if the backend
  needs it per edge, the columns have to become nullable.
