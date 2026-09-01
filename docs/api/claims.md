# F1 — Claim Repository Bank

All routes require a Bearer token.

**Terminology** (PRD §3) — the API uses the formal type names; the UI labels are
the short forms:

| API `claim_type` | PRD formal name | UI label | Scored? |
|---|---|---|---|
| `existing` | Existing Claim | Generic Claim | Yes — `final_claim_score` |
| `non_existing` | Non-Existing Claim | Synthetic Claim | No |

The AI service may write any of several aliases into `claims.claim_type`
(`generic`, `synthetic`, `predicted`, …); the API normalizes them to the two
values above. See [../AI-INTEGRATION.md](../AI-INTEGRATION.md).

**Status model** (PRD US1, v1.3) — one unified set shared by both types:
`unreviewed`, `active`, `inactive`, `action_taken`. The former type-specific
`debunk` / `prebunk` statuses were merged into `action_taken` and are rejected.

---

## GET /api/v1/claims/repository

The entire F1 page in one request: both sections plus the "last fetched" label.

**Both sections are always returned**, regardless of the selected status tab.
Per US1 the status filter narrows claims *within* each section and never hides a
section outright.

| Query | Default | Notes |
|---|---|---|
| `status` | `all` | US1 status tab. |
| `topic_ids` | — | Comma-separated UUIDs, multi-select (US6/US15). |
| `q` | — | Search claim text within each section (US11, US19). `%`/`_` are escaped, same as `GET /claims`. |

Each section returns at most **10** claims: S1 ranked by `final_claim_score`
descending (US7), S2 by newest first (US16). `total_in_pool` is the full
filtered count behind the "See all" button.

```bash
curl "http://localhost:8080/api/v1/claims/repository?status=all" \
  -H "Authorization: Bearer $TOKEN"
```

**200 OK**

```json
{
  "success": true,
  "message": "claim repository",
  "data": {
    "last_fetched_at": "2026-08-30T14:31:46Z",
    "applied_status": "all",
    "applied_topics": [],
    "existing": {
      "section": "S1",
      "label": "Existing Claim (Generic Claim)",
      "claim_type": "existing",
      "sorted_by": "final_claim_score DESC",
      "total_in_pool": 2,
      "claims": [
        {
          "id": "c0000000-0000-0000-0000-000000000002",
          "claim_type": "existing",
          "claim_statement": "Flood gates were deliberately opened to protect wealthy districts",
          "topic": { "id": "a0000000-0000-0000-0000-000000000002", "name": "Flood Response" },
          "review_status": "unreviewed",
          "created_at": "2026-08-30T14:32:45Z",
          "final_claim_score": 84.9,
          "first_caught_at": "2026-08-28T14:32:45Z",
          "positive_statement_count": 0,
          "negative_statement_count": 0,
          "is_dormant": true,
          "is_on_alert": false
        }
      ]
    },
    "non_existing": {
      "section": "S2",
      "label": "Non-Existing Claim (Synthetic Claim)",
      "claim_type": "non_existing",
      "sorted_by": "created_at DESC",
      "total_in_pool": 1,
      "claims": [
        {
          "id": "c0000000-0000-0000-0000-000000000003",
          "claim_type": "non_existing",
          "claim_statement": "The congestion charge revenue will be diverted to foreign contractors",
          "topic": { "id": "a0000000-0000-0000-0000-000000000001", "name": "Congestion Charge" },
          "review_status": "unreviewed",
          "created_at": "2026-08-30T14:32:45Z"
        }
      ]
    }
  }
}
```

> Note the Synthetic card carries **no** score, dates, statement counts, or bell
> state — US18 requires those fields to be absent, not zero.

### Card fields

| Field | Both | Existing only | Source |
|---|:-:|:-:|---|
| `id`, `claim_statement`, `claim_type`, `topic`, `created_at` | ✅ | | AI `claims` |
| `review_status` | ✅ | | Backend `cis_claim_reviews`, defaulting to `unreviewed` |
| `final_claim_score` | | ✅ | AI `claims.final_claim_score` |
| `first_caught_at` | | ✅ | AI `claims.first_caught_at` |
| `positive_statement_count` | | ✅ | Count of `content_items.stance = 'supporting'` |
| `negative_statement_count` | | ✅ | Count of `content_items.stance = 'opposing'` |
| `is_dormant` | | ✅ | AI `claims.is_dormant` |
| `is_on_alert` | | ✅ | Backend `cis_claim_alerts` — drives the bell icon (US14) |
| `coordinated_network` | | ✅ | F5 — drives the US61 indicator. **Omitted, not null, when nothing qualifies.** See below. |

**Positive / Negative statements** map to the `supporting` / `opposing` stance.
`neutral` content is excluded from both counts, mirroring the NPR definition in
PRD 6.4.2, so the counts always agree with the score.

---

## GET /api/v1/claims

The "See all" list (US8, US17), paginated.

| Query | Default | Notes |
|---|---|---|
| `type` | all | `existing`, `non_existing`, `all` |
| `status` | `all` | US1 status tab |
| `topic_ids` | — | Comma-separated UUIDs |
| `q` | — | Search claim text (US11, US19). `%`/`_` are escaped. |
| `sort` | by type | `score` or `created_at`. Defaults to `score` for Existing, `created_at` for Non-Existing. |
| `page`, `limit` | `1`, `20` | Max `limit` 200 |

```bash
curl "http://localhost:8080/api/v1/claims?type=existing&q=flood&limit=20" \
  -H "Authorization: Bearer $TOKEN"
```

**200 OK** — `data` is an array of the card objects above, with `meta` pagination.

---

## GET /api/v1/claims/:id

The claim detail page (US12 Existing / US20 Synthetic).

```bash
curl http://localhost:8080/api/v1/claims/c0000000-0000-0000-0000-000000000001 \
  -H "Authorization: Bearer $TOKEN"
```

**200 OK** (Existing claim, abridged)

```json
{
  "success": true,
  "message": "claim detail",
  "data": {
    "id": "c0000000-0000-0000-0000-000000000001",
    "claim_type": "existing",
    "claim_statement": "The congestion charge is a secret tax that will bankrupt small businesses",
    "topic": { "id": "a0000000-0000-0000-0000-000000000001", "name": "Congestion Charge" },
    "review_status": "action_taken",
    "review": {
      "notes": "Confirmed false; drafted a correction for the comms channel.",
      "reviewed_by": "d0000000-0000-0000-0000-000000000001",
      "reviewed_at": "2026-08-30T14:40:00Z"
    },
    "created_at": "2026-08-30T14:32:45Z",
    "updated_at": "2026-08-30T14:32:45Z",
    "activity": {
      "type": "debunk",
      "content": "Fact: the congestion charge is a published, debated policy.",
      "generated_at": "2026-08-30T14:32:45Z",
      "available": true,
      "debunk": {
        "core_fact": "The congestion charge is a published policy, debated in council and costed in public.",
        "nuanced_flag": "A claim is circulating that misrepresents how the charge is funded.",
        "reiterated_fact": "The charge and its revenue allocation are set out in the published policy document."
      }
    },
    "policies": [
      {
        "id": "b0000000-0000-0000-0000-000000000001",
        "name": "Jakarta Congestion Charge 2026",
        "source": "ai",
        "ai_policy_id": "b0000000-0000-0000-0000-000000000001",
        "has_document": false
      }
    ],
    "first_caught_at": "2026-08-25T14:32:45Z",
    "score_breakdown": {
      "reach": 72.5,
      "velocity": 61,
      "falseness": 88,
      "harm": 79,
      "emotional_intensity": 66,
      "emotional_intensity_opposing": 41,
      "harm_breakdown": {
        "public_safety": 85,
        "institutional_trust": 78,
        "economic": 70,
        "policy_disruption": 65,
        "human_confirmed": true,
        "weights": { "public_safety": 0.35, "institutional_trust": 0.3, "economic": 0.2, "policy_disruption": 0.15 }
      },
      "claim_score": 76.7,
      "npr": 0.22,
      "discount_factor": 0.89,
      "final_claim_score": 68.3,
      "is_dormant": false,
      "weights": { "reach": 0.15, "velocity": 0.15, "falseness": 0.3, "harm": 0.3, "emotional_intensity": 0.1 }
    },
    "top_accounts": [
      { "rank": 1, "author_id": "@driver_jkt", "content_count": 2, "total_impressions": 37000 },
      { "rank": 2, "author_id": "@warga_id", "content_count": 1, "total_impressions": 5000 }
    ],
    "positive_statement_count": 3,
    "negative_statement_count": 2,
    "is_on_alert": false
  }
}
```

### `coordinated_network` — the US61 indicator

Present on the claim detail **and on every claim card**, including the cards the
F2 policy detail page renders (US10, US39) — it is the same card everywhere, and
a policy-specific variant is explicitly forbidden.

```json
"coordinated_network": {
  "network_id": "8f1c...",
  "label": "Flood-gate amplification cluster",
  "coordination_score": 78.4,
  "confidence_band": "high",
  "review_status": "confirmed",
  "account_count": 47,
  "other_count": 1,
  "detail_url": "/api/v1/networks/8f1c..."
}
```

`review_status` is **displayed**, not merely used for filtering. US61's own
words: *"'Unreviewed, Medium' and 'Confirmed, High' must not read identically to
an analyst deciding whether to rebut or refer."*

Where more than one network qualifies, the highest-scoring one is returned and
`other_count` says how many others also qualify.

**The gate is a conjunction of four conditions**, all evaluated inside a single
grouped query, never as a post-filter:

1. a `network_claim_link` row exists for this claim with `passed_relevance_gate = true` — anchoring a run to a claim does not make what it finds *about* that claim;
2. the confidence band is `medium` or `high` — F1 has no low-confidence toggle, so `low` has no surface here;
3. the review status is **not** `dismissed_false_positive`;
4. the network is not suppressed under PRD 10.6.3 — a network invisible in F5 must not be reachable through F1.

When nothing qualifies the field is **omitted**, matching the PRD's "no empty
state" rule. A backend running without the detection pipeline deployed behaves
identically: no badge, no error. "The detector does not exist" and "no network
qualifies" look the same from F1, which is correct — in both cases there is
nothing to show.

**Why it matters more than it looks.** This is the point of F5 in daily use: it
decides whether the team publicly rebuts a claim or quietly refers it to the
platform. Rebutting a claim that only 40 accounts are actually making hands it
the reach it was engineered to get.

Full F5 reference: [networks.md](networks.md).

### `review`

The reviewer's decision behind the current `review_status`, read back from
`cis_claim_reviews`. `null` when the claim has never had a status set (i.e.
`review_status` is defaulting to `unreviewed`). This is a single overlay row
per claim, not a change log — it always reflects only the *most recent*
`PUT /claims/:id/status` call, so earlier notes are overwritten rather than
retained.

### `score_breakdown` — the Score Transparency Requirement (US23, PRD 6.5)

Every component is returned **together with** `final_claim_score`. The collapsed
number is never served without its inputs.

`weights` are included so the UI can explain the ranking without hardcoding
constants. **New in v1.5:** so is `formula`, the plain-language sentence behind
the US23 info-tooltip. It is served rather than written into the frontend so the
words and the weights can never drift apart — both are generated from the same
constants in `internal/scoring`.

**A note on this worked example:** `reach`, `velocity`, `falseness`, `harm`,
`harm_breakdown`, and `claim_score` are all written by the AI service and only
clamped/passed through here (`internal/scoring`) — this backend does not
compute any of them itself, so it cannot guarantee they satisfy PRD 6.2.4/6.3
for real data. `claim_score` above (`76.7`) and `final_claim_score` (`68.3`)
have been set to the values the formulas in 6.3/6.4.4 actually produce from the
other fields shown, so a reader who multiplies through gets a consistent
example. `harm` (`79`) still does not equal the weighted sum of
`harm_breakdown` (`0.35(85)+0.30(78)+0.20(70)+0.15(65) = 76.9`); since
`harm_breakdown.human_confirmed` is `true`, one plausible reading is that a
reviewer confirmed the four sub-scores but separately overrode the composite
`harm` value — **this needs confirming with the AI service integration, not
assumed**.

**Dormant claims** (US25, PRD 6.4.7): when `is_dormant` is `true`, `npr` and
`discount_factor` are `null` and a `note` explains why. A claim with no
supporting or opposing volume is *flagged*, never discounted — its priority must
not be lowered on the basis of statistically unreliable data.

```json
{
  "npr": null,
  "discount_factor": null,
  "final_claim_score": 84.9,
  "is_dormant": true,
  "note": "No supporting or opposing volume in the rolling window, so this claim is flagged dormant rather than discounted. NPR and DiscountFactor are not applicable (PRD 6.4.7)."
}
```

`emotional_intensity_opposing` is **diagnostic only** (US24, PRD 6.4.6). It is
displayed beside `emotional_intensity` but never enters any score.

**`harm_breakdown.edit` — the human-override audit trail (US23, new in v1.5).**
Present only once a reviewer has edited the Harm sub-scores; omitted while the
values are the AI's originals. That presence is what lets the UI mark an edited
H distinctly from an AI-original one wherever the score badge appears — the
`human_confirmed` boolean cannot, since it is also set by an empty confirmation.

```json
{
  "harm_breakdown": {
    "public_safety": 90,
    "human_confirmed": true,
    "edit": {
      "edited_by": "0f2c...",
      "edited_at": "2026-09-01T08:12:00Z",
      "previous": {
        "public_safety": 85,
        "institutional_trust": 78,
        "economic": 70,
        "policy_disruption": 65,
        "harm_score": 76.9
      }
    }
  }
}
```

`previous` holds the AI's classification before the override, so the original is
recoverable from the page as well as from the audit table
(`cis_claim_harm_edits`). Only the four Harm sub-components are editable; R, V,
F and EI remain AI-only.

For a Synthetic claim the response omits `score_breakdown`, `top_accounts`,
`first_caught_at`, the statement counts, and `is_on_alert`; `activity.type`
is `"prebunk"`.

> `activity` is served from the AI service's cache (`claims.activity_content`).
> Viewing a claim **never** triggers a new AI generation, per US12/US20.

### `activity.debunk` — the Truth Sandwich

The AI service writes the debunk twice: flat in `activity_content`, and split
into three labelled blocks. `content` stays the copyable single paragraph;
`debunk` is the same material as three sections the UI can label and lay out.

| Field | Block |
|---|---|
| `core_fact` | The true, verified fact — stated first |
| `nuanced_flag` | A brief, neutral note that a false claim is circulating, without repeating its specific wording |
| `reiterated_fact` | The fact restated in different words |

`debunk` is **omitted entirely** when the AI service has written none of the
three, which is the case for every Synthetic claim (their prebunk is flat) and
for any Existing claim generated before the split existed. An individual block
can be `null` when only some were written.

### `activity.segments` — segmented Debunk Activity (US12, new in v1.5)

v1.5 replaces the single generic draft with **one tailored recommendation per
audience segment** affected by the claim. The AI service identifies the segments
most exposed to it and generates one copy variant each, addressing that
segment's own framing — still generated once, at claim creation, and cached.

```json
{
  "activity": {
    "type": "debunk",
    "content": "…the flat, copyable single block…",
    "segments": [
      {
        "segment": "Kampung residents in flood-prone kelurahan",
        "rationale": "Highest exposure and the strongest distrust signal in the supporting cluster.",
        "content": "…copy written for this segment…",
        "generated_at": "2026-08-30T14:32:45Z"
      },
      {
        "segment": "Commuters on the affected corridor",
        "rationale": "Second-largest share of engagement; concern is journey time, not safety.",
        "content": "…different copy, different framing…",
        "generated_at": "2026-08-30T14:32:45Z"
      }
    ]
  }
}
```

Always an array, never `null` — a nullable list is a branch the frontend should
not have to write. It is **empty** for Synthetic claims (whose prebunk is not
segmented) and on a deployment whose AI service has not shipped segmentation
yet, where the page falls back to `content`. See
[sql/02_f6_reference_schema.sql](../sql/02_f6_reference_schema.sql).

Ordered most-exposed segment first. **Never merge the variants into one box** —
targeting is the entire point of the change, and a single box implying it
addresses "everyone" is the generic draft v1.5 removed.

---

## PUT /api/v1/claims/:id/harm/confirm

An analyst confirms or overrides the AI's four Harm sub-scores (PRD 6.2.4).
**Existing claims only.**

The backend cannot apply this itself — `harm_*`, `harm_human_confirmed` and every
score derived from them are columns on the AI-owned `claims` table — so the
request is proxied to the AI service, which recomputes
`harm_score → claim_score → final_claim_score` and appends a score snapshot. The
claim is then re-read from the database, so the response is the same full detail
payload `GET /claims/:id` returns.

Every field is optional and on a 0–100 scale. An omitted field keeps the AI's
own classification; **an empty body is valid** and is the "I reviewed these and
they are right" case, which still flips `harm_breakdown.human_confirmed` to
`true`.

```bash
curl -X PUT "http://localhost:8080/api/v1/claims/$ID/harm/confirm" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{ "public_safety": 90, "economic": 55 }'
```

**200 OK** — the full `ClaimDetail`, with

```json
{
  "score_breakdown": {
    "harm_breakdown": {
      "public_safety": 90,
      "institutional_trust": 78,
      "economic": 55,
      "policy_disruption": 65,
      "human_confirmed": true
    }
  }
}
```

| Status | When |
|---|---|
| `404` | Unknown claim |
| `422` | The claim is Synthetic — it carries no scores to confirm — or a sub-score is outside 0–100 |
| `503` | `AI_SERVICE_URL` is unset, or the AI service could not apply the change |

This call runs on `AI_SERVICE_LONG_TIMEOUT`: the AI service rescores the claim
before replying.

**Two things happen on this side once the AI service accepts the change**
(US23's system flow, new in v1.5):

1. An audit row is appended to `cis_claim_harm_edits` recording who edited it,
   when, and the four values as they were before. It is written *after* the AI
   service accepts — an audit entry for an edit that failed is worse than none —
   and it surfaces as `harm_breakdown.edit` on every later read.
2. The claim is re-evaluated against the alert threshold. Recomputing H moves
   `final_claim_score`, which can push a watched claim across it; waiting for the
   hourly snapshot job would leave an edit made at 09:05 unnotified until 10:00.
   A resulting crossing shows up in
   [`GET /alerts/notifications`](alerts.md#get-apiv1alertsnotifications).

---

## GET /api/v1/claims/:id/statements

Paginated source posts behind a claim (US12).

| Query | Default | Values |
|---|---|---|
| `stance` | `all` | `positive` (= supporting), `negative` (= opposing), `neutral`, `all` |
| `page`, `limit` | `1`, `20` | |

```bash
curl "http://localhost:8080/api/v1/claims/$ID/statements?stance=negative" \
  -H "Authorization: Bearer $TOKEN"
```

**200 OK**

```json
{
  "success": true,
  "message": "claim statements",
  "data": [
    {
      "id": "6f1c...",
      "text": "Actually revenue funds buses",
      "source": "twitter",
      "author_id": "@transit_fact",
      "location": null,
      "stance": "opposing",
      "outrage_score": null,
      "impressions": 30000,
      "positive_reaction_count": null,
      "negative_reaction_count": null,
      "created_at": "2026-08-30T14:32:45Z"
    }
  ],
  "meta": { "page": 1, "limit": 20, "total": 2, "total_pages": 1 }
}
```

---

## GET /api/v1/claims/:id/top-accounts

The Top 5 Accounts panel (US12).

Ranked over **Supporting-side** content only, matching the Reach parameter's
scope in PRD 6.1.1, ordered by contributed impressions with post count as the
tiebreaker.

| Query | Default |
|---|---|
| `limit` | `5` |

**200 OK**

```json
{
  "success": true,
  "message": "top accounts",
  "data": [
    { "rank": 1, "author_id": "@driver_jkt", "content_count": 2, "total_impressions": 37000 }
  ]
}
```

> The PRD flags this requirement's source instruction as truncated and asks for
> confirmation. This implements the documented interpretation — *accounts
> driving the claim's spread*. If you intend something else (opposing accounts,
> engagement-ranked, or bot-like accounts), only the ordering in
> `ListTopAccounts` needs to change.

---

## GET /api/v1/claims/:id/policies

Correlated public policies. Many-to-many for Existing claims (US12), one-to-many
for Synthetic claims (US20).

`source` distinguishes where the policy record came from:

- `cis` — registered through F2. Includes `status`, `rolled_out_date`, and `has_document`.
- `ai` — created directly by the AI service, with no F2 upload behind it.

---

## GET /api/v1/claims/:id/score-history

> **Two sources, merged.** Points come from the backend's own hourly snapshots
> (`cis_claim_score_snapshots`, captured for watched claims only) **and** from
> the AI service's `claim_score_snapshots`, which it appends every time it
> rescores any claim. A claim that was never bell-icon'd would otherwise return
> an empty series. Values are averaged per bucket across both sources, weighted
> by the number of underlying rows. `claim_score` comes only from backend
> snapshots — the AI table records the final score alone — so it can be `null`
> in a bucket where `final_claim_score` is not.


`final_claim_score` over time. This backs the **Score History Chart** US12 adds
to the claim detail page in v1.5.

| Query | Default | Values |
|---|---|---|
| `granularity` | `week` | `day`, `week`, `month`, `year` |
| `from`, `to` | — | RFC3339 or `YYYY-MM-DD` |

`granularity` is the Day/Week/Month/Year selector. US27 requires the Alert
page's `[C1]` chart to reuse **this same control**, so
[`GET /alerts/chart`](alerts.md#get-apiv1alertschart) takes the identical four
values — one component on the frontend, one parameter on the backend.

```json
{
  "success": true,
  "message": "claim score history",
  "data": {
    "claim_id": "c0000000-0000-0000-0000-000000000001",
    "granularity": "month",
    "points": [
      { "bucket_start": "2026-08-01T00:00:00Z", "final_claim_score": 68.9, "claim_score": 77.4, "sample_count": 1 }
    ]
  }
}
```

History only exists from the moment a claim is added to the F3 watchlist — the
snapshot job only captures watched claims. Scores are averaged within each
bucket; `sample_count` tells you how many snapshots contributed.

---

## PUT /api/v1/claims/:id/status

Records a reviewer's decision (US10 for Existing, US18 for Synthetic).

**Body**

| Field | Type | Rules |
|---|---|---|
| `status` | string | required — `unreviewed`, `active`, `inactive`, `action_taken` |
| `notes` | string | optional, ≤2000 |

```bash
curl -X PUT "http://localhost:8080/api/v1/claims/$ID/status" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"status":"action_taken","notes":"Debunk published on 30 Aug"}'
```

**200 OK**

```json
{
  "success": true,
  "message": "claim status updated",
  "data": {
    "claim_id": "c0000000-0000-0000-0000-000000000001",
    "review_status": "action_taken",
    "notes": "Debunk published on 30 Aug",
    "reviewed_at": "2026-08-30T14:33:24Z",
    "reviewed_by": "21c4bbdd-f208-4696-a467-9f0edc23e910"
  }
}
```

> **This writes `cis_claim_reviews`, not the AI service's `claims.status`.**
> The AI's own pipeline state is left untouched, so re-running detection can
> never silently overwrite a human decision, and you get a free audit trail of
> who changed what and when.

**Errors** — `404 NOT_FOUND` unknown claim · `400 VALIDATION_FAILED` for a
status outside the four allowed values (including the retired `debunk` /
`prebunk`) — caught by request validation before the handler runs, so this is
the only code you will see for that condition, never `422`.
