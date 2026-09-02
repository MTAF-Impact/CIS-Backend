# F4 — Admin Setting Page

All routes require a Bearer token. There are **no roles** in this build, so any
authenticated user can change these settings — see [auth.md](auth.md).

---

## GET /api/v1/settings

Lists every global setting with its audit metadata.

```bash
curl http://localhost:8080/api/v1/settings -H "Authorization: Bearer $TOKEN"
```

**200 OK**

```json
{
  "success": true,
  "message": "settings",
  "data": [
    {
      "key": "alert_threshold",
      "value": "70",
      "value_type": "number",
      "description": "Global FinalClaimScore threshold (0-100) deciding Over/Under Threshold on the Alert page (PRD US32).",
      "updated_at": "2026-08-30T14:31:46Z",
      "updated_by": null
    },
    {
      "key": "claims_last_fetched_at",
      "value": "2026-08-30T14:31:46Z",
      "value_type": "timestamp",
      "description": "Timestamp shown as 'last fetched' on the Existing Claim section (PRD US9/US33).",
      "updated_at": "2026-08-30T14:31:46Z",
      "updated_by": null
    }
  ]
}
```

---

## GET /api/v1/settings/alert-threshold

The current global Over/Under Threshold cutoff (US32).

```json
{
  "success": true,
  "message": "alert threshold",
  "data": { "threshold": 70, "updated_at": "2026-08-30T14:31:46Z", "updated_by": null }
}
```

Defaults to `70` on a fresh database, so F3 never breaks before an admin has
saved anything.

---

## PUT /api/v1/settings/alert-threshold

Sets the threshold (US32).

**Body**

| Field | Type | Rules |
|---|---|---|
| `threshold` | number | required, `0`–`100` |

```bash
curl -X PUT http://localhost:8080/api/v1/settings/alert-threshold \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"threshold":60}'
```

**200 OK**

```json
{
  "success": true,
  "message": "alert threshold updated",
  "data": {
    "threshold": 60,
    "updated_at": "2026-08-30T14:37:26Z",
    "updated_by": "21c4bbdd-f208-4696-a467-9f0edc23e910"
  }
}
```

**This applies globally and takes effect immediately.** Every claim's
`threshold_status` on F3 is derived at read time, so lowering the threshold from
70 to 60 instantly flips a claim scoring 68.9 from `under_threshold` to
`over_threshold` with no recomputation.

The range is fixed to 0–100 because the threshold is compared against
`final_claim_score`, which PRD 6.5 pins to that scale.

**Errors** — `400 VALIDATION_FAILED` outside 0–100 — caught by request
validation before the handler runs, so this is the only code you will see for
that condition, never `422`.

---

# US65 — City configuration

**New in PRD v1.5.** Which single Indonesian city this instance is monitoring.
Only one is active at a time; selecting a new one replaces the previous
selection outright — there is no concurrent multi-city state in this phase.

It scopes the entire [F6 Overview page](overview.md): the Climate Sentiment
Index (O1), the topic treemap (O2) and the policy leaderboard (O3).

The city list is a closed set held in code (`internal/models/f6_cities.go`), not
a table. It is reference data that changes on a human timescale, and making it a
table would invite a second source of truth against the IANA zone the F5 report
footer already needs.

## GET /api/v1/settings/cities

The dropdown's options, plus the current selection.

```json
{
  "success": true,
  "message": "configurable cities",
  "data": {
    "cities": [
      { "name": "Jakarta", "province": "DKI Jakarta", "timezone": "Asia/Jakarta" },
      { "name": "Makassar", "province": "Sulawesi Selatan", "timezone": "Asia/Makassar" }
    ],
    "selected": { "name": "Jakarta", "province": "DKI Jakarta", "timezone": "Asia/Jakarta" }
  }
}
```

## GET /api/v1/settings/city

The current selection alone, in the same shape as `selected` above.

## PUT /api/v1/settings/city

**Body**

| Field | Type | Rules |
|---|---|---|
| `city` | string | required — must match a `name` from `GET /settings/cities`, case-insensitively |

```bash
curl -X PUT http://localhost:8080/api/v1/settings/city \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"city":"Makassar"}'
```

**Selecting a city also sets `city_timezone`.** Before v1.5 those were
independent settings, which meant an instance could be monitoring Makassar while
stamping its F5 reports in Jakarta time. US65 gives the city one source of
truth, and the timezone follows it. The change is recorded in the setting
history like any other governed change.

**Errors** — `422 UNPROCESSABLE_ENTITY` for a city outside the list, naming
`GET /api/v1/settings/cities` in the message.

### What the city actually filters

If the AI service tags content with a city
([sql/02_f6_reference_schema.sql](../sql/02_f6_reference_schema.sql)), the
selection genuinely partitions F6: a claim belongs to the city when any content
backing it does. If it does not, the selection **labels** the instance instead,
and `city.partitioned` on the F6 response is `false` so the UI can say so. PRD
6.6.4 already scopes this phase to one city at a time, so an untagged deployment
is the single-city instance the PRD describes rather than a broken one.

**Today it labels.** The AI team has deferred `content_items.city` until a second
city is actually configured, for exactly the reason above — so expect
`city.partitioned: false`, and treat the labelling branch as the live one rather
than the fallback.

---

# The detector control panel (US62)

F4 grew from one threshold to roughly thirty governed parameters. They do **not**
live in `cis_settings`: a flat key/value store cannot express the two cross-field
constraints, and both of them matter.

- **The five signal weights must sum to 1.00.** `beta_time + beta_text +
  beta_amp + beta_meta + beta_struct`. A composite built from weights summing to
  0.9 is not on the 0–100 scale it claims to be on.
- **The run cadence must be ≤ W/2.** PRD 10.5.1 requires consecutive detection
  windows to overlap by 50%, so behaviour straddling a window boundary is not
  split across two runs and missed by both. `W` (1–30 days) and the cadence
  (1–24 h) are independently configurable, so an admin can otherwise legally set
  `W = 1 day` with a 24 h cadence and open a boundary blind spot every midnight.

Range validation lives in Go rather than in struct tags for the same reason: a
tag cannot see a sibling field.

## GET /api/v1/settings/detector

Every parameter, plus `updated_at`, `updated_by`, and `self_exclusion_count` —
how many accounts are excluded as the city's own comms estate, managed through
the allowlist under its own category.

## PUT /api/v1/settings/detector

**Every field is optional.** An omitted parameter keeps its stored value: a
screen that saves one threshold must not silently reset the other twenty-nine to
whatever its form defaulted to.

```bash
curl -X PUT http://localhost:8080/api/v1/settings/detector   -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json'   -d '{"window_days": 14, "cadence_hours": 12}'
```

**422** when a value is out of range or a cross-field constraint fails, with the
constraint written out:

```json
{
  "success": false,
  "message": "detector settings failed validation",
  "error": {
    "code": "UNPROCESSABLE_ENTITY",
    "details": {
      "cadence_hours": "consecutive runs must overlap by 50% of the window (PRD 10.5.1), so the cadence may not exceed 84 hours for a 7-day window"
    }
  }
}
```

Validation is whole-row, not per-field: the stored row is merged with the
submitted changes and the result is validated as a unit. Sending a new
`window_days` that invalidates the *stored* cadence fails, which is the point —
the pair has to be legal together.

**Changing a parameter never retroactively alters a stored detection.** Each
`detection_run` carries the whole parameter set that was in force when it ran, so
a report generated months later reads its configuration from the run rather than
from the current settings. This is a US62 requirement, not an optimisation.

Changes take effect on the **next** detection tick, not on the next restart — the
scheduler reads the cadence from this row on every tick.

## GET /api/v1/settings/detector/ranges

The min, max, default and label for each parameter, straight from PRD 10.11's
Default Parameter Reference. Serve the form from this rather than hard-coding
bounds in the client, so the two cannot disagree about what is legal.

## GET /api/v1/settings/detector/history

Every parameter change with its old value, new value, user and timestamp.
`GET /api/v1/settings/history` returns the same log across all settings.

## GET|PUT /api/v1/settings/city-timezone

An IANA zone name, e.g. `Asia/Jakarta`. PRD 10.8 requires every report page
footer to carry the generation time in UTC **and** city-local time, and nothing
else in the system knows which city. An invalid zone name is rejected with `422`
rather than silently falling back to UTC, which would put a wrong local time in
a document that is going to a platform.

Since v1.5 this is normally set for you: `PUT /api/v1/settings/city` writes the
selected city's zone here. Setting it directly still works and is the escape
hatch for a deployment whose city is not in the US65 catalog.

The rest of F5 is documented in [networks.md](networks.md).

---

## POST /api/v1/admin/generate-generic-claim

The "Generate Generic Claim" MVP test-data button (US33).

**Body** (optional)

| Field | Type | Rules |
|---|---|---|
| `topic_id` | string | optional UUID — pins the claim to an existing topic |

**v1.5 cross-reference (US33).** The generated claim must exercise both v1.5
additions to be useful as demo data: its Debunk Activity needs at least one
segmented recommendation (US12) and its four Harm sub-scores must be present so
they can be edited (US23). Both are produced by the AI service, which owns the
generator — this endpoint proxies onto it. See
[AI-INTEGRATION.md](../AI-INTEGRATION.md).

```bash
curl -X POST http://localhost:8080/api/v1/admin/generate-generic-claim \
  -H "Authorization: Bearer $TOKEN"
```

**201 Created**

```json
{
  "success": true,
  "message": "generic claim generated",
  "data": {
    "claim_id": "9f2c...",
    "claim_statement": "The new emissions rule will shut down all delivery vans",
    "topic_id": "a0000000-0000-0000-0000-000000000001",
    "last_fetched_at": "2026-08-30T14:40:02Z",
    "message": "generic claim generated"
  }
}
```

On success the S1 "last fetched" timestamp (US9) is moved to the moment the
button was clicked, exactly as US33 requires.

> **This endpoint proxies to the AI service.** The backend cannot create the
> claim itself: `claims` is owned and written exclusively by the AI service, and
> this backend never writes AI-owned tables. The AI service inserts a fully
> populated claim — score breakdown, statements, top accounts, cached debunk
> draft, its `claim_debunk_segments` rows, and a `claim_policies` link so the
> Related Policies panel is populated too — and returns its id. It builds the
> claim through the same construction and scoring pipeline real clustering uses,
> not a parallel path, so nothing about a demo claim can drift from a real one.
>
> Expect **30–60 seconds**: it is several sequential LLM calls, which is why this
> runs on `AI_SERVICE_LONG_TIMEOUT`.

**503 SERVICE_UNAVAILABLE** when `AI_SERVICE_URL` is not configured:

```json
{
  "success": false,
  "message": "the Generate Generic Claim utility requires the AI service, because claims are owned and written exclusively by it. Set AI_SERVICE_URL to enable this button.",
  "error": { "code": "SERVICE_UNAVAILABLE" }
}
```

Style this button as a distinct MVP/testing utility (Glaucous, per PRD §5.6).

---

## POST /api/v1/admin/snapshot-scores

Immediately captures a score snapshot for every watched claim, building the F3
chart history without waiting for the hourly cron job. Useful for demos.

```bash
curl -X POST http://localhost:8080/api/v1/admin/snapshot-scores \
  -H "Authorization: Bearer $TOKEN"
```

**200 OK**

```json
{ "success": true, "message": "score snapshots captured", "data": { "snapshots_captured": 2 } }
```

Returns `0` when the watchlist is empty — only watched claims are captured, since
they are the only ones F3 charts.

Note this captures *current* scores; it does not recompute them. The hourly cron
job calls the AI service's rescore first — see below — so a manual snapshot
straight after a manual rescore reproduces what the cron does.

---

## POST /api/v1/admin/rescore

Asks the AI service to re-evaluate every Existing claim's score.

```bash
curl -X POST http://localhost:8080/api/v1/admin/rescore \
  -H "Authorization: Bearer $TOKEN"
```

**200 OK**

```json
{ "success": true, "message": "claims rescored", "data": { "claims_rescored": 4 } }
```

**Why this exists.** A claim's score moves with wall-clock time even when
nothing new is ingested: NPR drifts as opposing posts age out of the rolling
window, which changes the discount factor and therefore `final_claim_score`. But
nothing recomputes that on a schedule — the AI service has no cron of its own,
and clustering only runs when content arrives. So the backend's hourly snapshot
job calls this **first** and captures afterwards; without that, the F3 trend
chart would plot the same number every hour, a horizontal line by construction.

This endpoint is the same trigger, run by hand. It runs on
`AI_SERVICE_LONG_TIMEOUT`.

**503** when `AI_SERVICE_URL` is unset — every score column belongs to the AI
service.

---

## POST /api/v1/admin/generate-sample-content

The "Generate sample data" button: populates the databank with fabricated but
realistic content, run through the same embed → analyze → cluster pipeline real
crawled content would be.

**Body** (optional)

| Field | Type | Default | Rules |
|---|---|---|---|
| `count` | int | `10` | 1–50 |
| `topic_hint` | string | — | max 255 chars; steers what the content is about |
| `auto_cluster` | bool | `true` | when true, clustering runs synchronously so the response can report the resulting claim counts |

```bash
curl -X POST http://localhost:8080/api/v1/admin/generate-sample-content \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{ "count": 10, "topic_hint": "road pricing" }'
```

**201 Created**

```json
{
  "success": true,
  "message": "sample content generated",
  "data": {
    "generated_count": 10,
    "failed_count": 0,
    "claims_created": 2,
    "claims_updated": 1,
    "content_items_clustered": 10,
    "last_fetched_at": "2026-08-31T09:14:02Z",
    "message": "generated 10 content items"
  }
}
```

The three `claims_*` / `content_items_clustered` counts are `null` when
`auto_cluster` was `false`: nothing was clustered, which is different from
clustering that produced nothing. Like the claim generator, this moves the S1
"last fetched" timestamp — new content means new claims.

> **This is the only way content enters the system through the product.**
> Outside of policy matchmaking's predicted claims and the single demo claim
> above, nothing a backend endpoint can trigger populates `content_items` — and
> therefore nothing else populates Existing claims.
>
> A real crawler now exists on the AI side — a separate Cloud Run Job pulling RSS
> and public Telegram channels into `POST /ingest/batch` — but it is not yet fed:
> its feed and channel lists, and its Telegram credentials, need manual curation
> before it produces anything. Until that is done, this button remains the
> practical source of content, and it stays useful afterwards for demos.
>
> The AI service's plain `/ingest` and `/ingest/batch` endpoints are
> deliberately **not** proxied. Those are that crawler's interface; it calls the
> AI service directly rather than routing content through a human-facing,
> JWT-authenticated backend.

Long-running with `auto_cluster` on: it uses `AI_SERVICE_LONG_TIMEOUT`.

**422** when `count` is outside 1–50. **503** when `AI_SERVICE_URL` is unset.

---

## POST /api/v1/admin/cluster-now

Forces a clustering pass over content the AI service has ingested but not yet
grouped into claims.

```bash
curl -X POST http://localhost:8080/api/v1/admin/cluster-now \
  -H "Authorization: Bearer $TOKEN"
```

**200 OK**

```json
{
  "success": true,
  "message": "clustering pass complete",
  "data": { "claims_created": 2, "claims_updated": 1, "content_items_clustered": 8 }
}
```

Normally unnecessary — ingestion triggers clustering on its own, in the
background. Useful after an ingest whose background pass has not finished, or to
force one without waiting.

---

## POST /api/v1/admin/reconcile

Clears backend rows whose AI-side claim or policy no longer exists.

**Body** (optional)

| Field | Type | Default | Meaning |
|---|---|---|---|
| `dry_run` | bool | `false` | Report what would be cleared without clearing it |
| `force` | bool | `false` | Override the empty-database guard described below |

```bash
curl -X POST http://localhost:8080/api/v1/admin/reconcile \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{ "dry_run": true }'
```

**200 OK**

```json
{
  "success": true,
  "message": "7 rows would be reconciled",
  "data": {
    "dry_run": true,
    "orphaned_reviews": 2,
    "orphaned_alerts": 1,
    "orphaned_score_snapshots": 3,
    "policies_unlinked": 1,
    "claims_in_database": 42,
    "ai_policies_in_database": 5,
    "message": "7 rows would be reconciled"
  }
}
```

**What it fixes.** Every backend reference into an AI table is a soft one, with
no foreign key — the backend must never constrain a table it does not own. So
when the AI side runs a demo reseed or a schema reset (both of which correctly
leave `cis_*` alone), nothing cascades:

| Left dangling | Symptom |
|---|---|
| `cis_claim_reviews.claim_id` | Review decisions for claims that no longer exist |
| `cis_claim_alerts.claim_id` | F3 lists watchlist rows pointing at nothing |
| `cis_claim_score_snapshots.claim_id` | History for deleted claims |
| `cis_policies.ai_policy_id` | F2 shows a "completed" badge above empty claim lists |

The first three are deleted. The fourth is not merely unlinked but **re-queued**
— `ai_policy_id` is cleared and `processing_status` goes back to `pending`
(or `skipped` when no AI service is configured), so matchmaking can rebuild the
correlations.

**The empty-database guard.** If the AI tables are present but empty, every
backend reference looks orphaned and a full sweep would erase the entire human
layer — every review decision, every watchlist entry. From here that is
indistinguishable from being pointed at the wrong database, so the sweep refuses:

```json
{
  "success": false,
  "message": "refusing to reconcile: the AI service's claims table is empty, so every backend review, watchlist entry and snapshot looks orphaned (48 rows). This usually means the backend is pointed at the wrong database. Pass force=true only if the AI data really was wiped deliberately.",
  "error": { "code": "CONFLICT" }
}
```

Check `DATABASE_URL` first. Pass `force: true` only when the AI data really was
wiped on purpose.

Prefer `dry_run: true` before a real run. Nothing here is recoverable.
