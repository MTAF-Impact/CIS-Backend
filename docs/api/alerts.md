# Alert Page

All routes require a Bearer token.

The Alert page is the watchlist for claims requiring ongoing monitoring. Its
layout is a line chart `[C1]`, a key/legend `[C2]`, and the watchlist table
`[C3]`.

**Only Existing/Generic claims can be watched.** Synthetic claims are
predictions that may never materialize, so adding one is rejected with `422`.

Claims can only join the watchlist through the claim repository's bell-icon
confirmation flow — there is deliberately no "add" action inside the Alert
page itself, but that flow is served by `POST /api/v1/alerts` below.

---

## GET /api/v1/alerts

The `[C3]` watchlist table.

Ordered by **most recently appended first**.

| Query | Default | Notes |
|---|---|---|
| `q` | — | Search by claim statement |
| `page`, `limit` | `1`, `20` | |

```bash
curl "http://localhost:8080/api/v1/alerts" -H "Authorization: Bearer $TOKEN"
```

**200 OK**

```json
{
  "success": true,
  "message": "alert watchlist",
  "data": [
    {
      "id": "c0000000-0000-0000-0000-000000000002",
      "alert_id": "8b1f...",
      "claim_statement": "Flood gates were deliberately opened to protect wealthy districts",
      "claim_created_at": "2026-08-30T14:32:45Z",
      "added_at": "2026-08-30T14:37:12Z",
      "chart_visible": false,
      "topic": { "id": "a0000000-0000-0000-0000-000000000002", "name": "Flood Response" },
      "review_status": "unreviewed",
      "final_claim_score": 84.9,
      "threshold_status": "over_threshold",
      "threshold": 70,
      "is_dormant": true,
      "just_crossed": true,
      "crossed_direction": "up",
      "crossed_at": "2026-09-01T08:00:00Z"
    }
  ],
  "meta": { "page": 1, "limit": 20, "total": 2, "total_pages": 1 }
}
```

`id` is the **claim id** — it is what the table's ID column displays and what
every other endpoint here takes as `:claimId`.

### `threshold_status`

Derived by comparing `final_claim_score` against the global alert threshold,
which is echoed back as `threshold` so the UI never has to fetch it
separately:

- `over_threshold` — `final_claim_score >= threshold`
- `under_threshold` — otherwise, **including when the score is `null`**. An
  unscored claim is not escalated on missing data.

Changing the global threshold immediately changes these values; no
recomputation is needed. Style `over_threshold` with the controlled
low-saturation red, and `under_threshold` with Mint Leaf.

### `just_crossed` — new in v1.5

`true` while this claim's Over/Under status has flipped **since the reader last
opened the Alert page**. It drives a light row highlight — distinct from, and
lighter than, the standing `over_threshold` status colour.

It is per-reader: one operator acknowledging a crossing must not clear a
colleague's highlight. `crossed_at` and `crossed_direction` (`up` = below →
above, `down` = above → below) stay on the row after acknowledgment, so the
table can still show what last happened to a claim; only `just_crossed` clears.

---

## GET /api/v1/alerts/notifications

**New in v1.5.** The sidebar counter badge on the "Alert" nav item.

```bash
curl "http://localhost:8080/api/v1/alerts/notifications" -H "Authorization: Bearer $TOKEN"
```

**200 OK**

```json
{
  "success": true,
  "message": "alert notifications",
  "data": {
    "unacknowledged_count": 2,
    "acknowledged_at": "2026-08-31T17:04:00Z",
    "threshold": 70,
    "crossings": [
      {
        "id": "c0000000-0000-0000-0000-000000000002",
        "claim_statement": "Flood gates were deliberately opened...",
        "final_claim_score": 84.9,
        "threshold_status": "over_threshold",
        "just_crossed": true,
        "crossed_direction": "up",
        "crossed_at": "2026-09-01T08:00:00Z"
      }
    ]
  }
}
```

`unacknowledged_count` is the badge number. `crossings` names the claims behind
it — newest first, capped at 20 — so the badge can be expanded into something
readable rather than only counted. A watchlist where dozens crossed at once is a
threshold problem, not a paging problem.

### When crossings are detected

On each score refresh: the hourly snapshot job re-evaluates every watched
claim's Over/Under status against the status recorded at the previous pass, and
stamps the ones that flipped. A Harm sub-score edit (`PUT
/claims/:id/harm/confirm`) re-evaluates that claim immediately, rather than
leaving an edit made at 09:05 unnotified until 10:00.

A crossing fires **only for a genuine transition**, never for a claim already
steady above or below the threshold. A claim just added to the watchlist has its
current status recorded as a baseline without notifying — a first sighting is
not a transition.

---

## POST /api/v1/alerts/notifications/acknowledge

**New in v1.5.** Clears this user's badge and row highlights.

Opening the Alert page counts as the acknowledgment, so the frontend calls
this on entering the page — *after* rendering the list it was handed, since
acknowledging is what makes the next render unhighlighted. A per-row dismiss
was considered as an alternative; that would be a different endpoint shape,
not a different mechanism.

Returns the same payload as `GET /api/v1/alerts/notifications`, now with
`unacknowledged_count: 0`.

---

## POST /api/v1/alerts

Adds a claim to the watchlist. Called after the user confirms the bell-icon
dialog on a claim or policy card.

**Body**

| Field | Type | Rules |
|---|---|---|
| `claim_id` | string | required, UUID |

```bash
curl -X POST http://localhost:8080/api/v1/alerts \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"claim_id":"c0000000-0000-0000-0000-000000000001"}'
```

**201 Created**

```json
{
  "success": true,
  "message": "claim added to the alert watchlist",
  "data": {
    "claim_id": "c0000000-0000-0000-0000-000000000001",
    "on_watchlist": true,
    "chart_visible": false,
    "added_at": "2026-08-30T14:37:12Z"
  }
}
```

`chart_visible` starts `false`: the chart stays empty until the user
explicitly checks a claim.

Adding a claim that is already watched is **not** an error — it returns `201`
with the existing `added_at`, so a double-click leaves the bell filled rather
than throwing.

**Errors**

- `422 UNPROCESSABLE_ENTITY` — a Synthetic claim was submitted:
  `only Existing (Generic) claims can be added to the Alert page; Non-Existing (Synthetic) claims are predictions and cannot be watched`
- `404 NOT_FOUND` — unknown claim.

---

## DELETE /api/v1/alerts/:claimId

Removes a claim from the watchlist.

This also clears its chart checkbox, so removing a claim unchecks it from
`[C1]`/`[C2]`.

```bash
curl -X DELETE "http://localhost:8080/api/v1/alerts/$CLAIM_ID" \
  -H "Authorization: Bearer $TOKEN"
```

**200 OK**

```json
{
  "success": true,
  "message": "claim removed from the alert watchlist",
  "data": { "claim_id": "c0000000-...", "on_watchlist": false, "chart_visible": false }
}
```

**Errors** — `404 NOT_FOUND` if the claim is not on the watchlist.

---

## PATCH /api/v1/alerts/:claimId/chart

Toggles the `[C3]` "Chart" checkbox that decides which claims `[C1]` and `[C2]`
render.

**Body**

| Field | Type | Rules |
|---|---|---|
| `visible` | boolean | required |

```bash
curl -X PATCH "http://localhost:8080/api/v1/alerts/$CLAIM_ID/chart" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"visible":true}'
```

**200 OK**

```json
{
  "success": true,
  "message": "chart visibility updated",
  "data": { "claim_id": "c0000000-...", "on_watchlist": true, "chart_visible": true }
}
```

**Errors** — `404 NOT_FOUND` if the claim is not on the watchlist.

---

## GET /api/v1/alerts/chart

The `[C1]` line chart and `[C2]` key.

Returns **only** claims currently checked via `chart_visible`. With none
checked, `series` is `[]` — that is the documented default empty state, not an
error.

| Query | Default | Values |
|---|---|---|
| `granularity` | `week` | `day`, `week`, `month`, `year` |
| `from`, `to` | — | RFC3339 or `YYYY-MM-DD` |

`granularity` backs the Day/Week/Month/Year selector added to `[C1]` in v1.5.
It is the **same four values** accepted by `GET /claims/:id/score-history`: one
shared selector component, introduced for the claim detail page's Score
History Chart, now reused here — one control, one parameter, two charts.

```bash
curl "http://localhost:8080/api/v1/alerts/chart?granularity=week" \
  -H "Authorization: Bearer $TOKEN"
```

**200 OK**

```json
{
  "success": true,
  "message": "alert chart",
  "data": {
    "granularity": "week",
    "threshold": 70,
    "y_axis_min": 0,
    "y_axis_max": 100,
    "series": [
      {
        "claim_id": "c0000000-0000-0000-0000-000000000001",
        "claim_statement": "The congestion charge is a secret tax that will bankrupt small businesses",
        "topic": { "id": "a0000000-0000-0000-0000-000000000001", "name": "Congestion Charge" },
        "points": [
          { "bucket_start": "2026-08-24T00:00:00Z", "final_claim_score": 68.9, "claim_score": 77.4, "sample_count": 1 }
        ]
      }
    ]
  }
}
```

`y_axis_min` / `y_axis_max` are fixed at 0–100 so the axis does not rescale as
claims are added or removed. `threshold` is included so a reference
line can be drawn without a second request.

### Where the history comes from

The AI service stores only a claim's **current** score. The chart needs a time
series, so a background job periodically copies the scores of every watched
claim into this backend's own `cis_claim_score_snapshots` table.

Consequences worth knowing:

- **History starts when a claim is added to the watchlist.** A newly watched
  claim has no past data, and its line begins at the next snapshot.
- Capture frequency is `CRON_SCORE_SNAPSHOT_SPEC` (hourly by default).
- Scores are **averaged** within each bucket; `sample_count` reports how many
  snapshots contributed.
- Snapshots older than ~400 days are pruned automatically.
- `POST /api/v1/admin/snapshot-scores` triggers a capture immediately, which is
  useful for demos.
