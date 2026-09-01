# F6 — Overview

All routes require a Bearer token.

**New in PRD v1.5.** F6 is the leadership summary across the whole claim
repository, and the **first item in the sidebar** (US66) — ahead of F1–F5. It
has three sections:

| Section | What it shows | Story |
|---|---|---|
| **O1** | Above/below-threshold claim ratio, and the Climate Sentiment Index gauge | US67, US68 |
| **O2** | Topic treemap, sized by a combined risk metric | US69 |
| **O3** | Top public policies attracting high-risk claims | US70 |

Everything on the page is scoped to the single Indonesian city configured in F4
(US65, [settings.md](settings.md#us65--city-configuration)).

Nothing here is stored. Every figure is computed from the same `claims` rows F1
ranks and the same content stream the AI service scores, on each request — a
cached copy would be a second number able to disagree with the page it
summarises.

---

## GET /api/v1/overview

The whole page in one call, mirroring `GET /claims/repository`: all three
sections are read together on every load.

| Query | Default | Notes |
|---|---|---|
| `limit` | `5` | Size of the O3 leaderboard. US70's section heading says "Top 10" and its detail says top 5; the detail wins and this parameter settles the difference without a redeploy. |

```bash
curl "http://localhost:8080/api/v1/overview" -H "Authorization: Bearer $TOKEN"
```

**200 OK**

```json
{
  "success": true,
  "message": "overview",
  "data": {
    "city": {
      "name": "Jakarta",
      "province": "DKI Jakarta",
      "timezone": "Asia/Jakarta",
      "partitioned": true
    },
    "generated_at": "2026-09-01T09:00:00Z",
    "threshold_ratio": {
      "above": 12,
      "below": 47,
      "total": 59,
      "above_percent": 20.33,
      "threshold": 70
    },
    "sentiment": {
      "status": "ok",
      "score": 61.4,
      "band": "watch",
      "bcs": 0.12,
      "bcs_normalized": 56,
      "risk_load": 33.2,
      "momentum": -1.8,
      "momentum_direction": "down",
      "volume": { "total": 8421, "positive": 2210, "negative": 1199, "neutral": 5012 },
      "window_start": "2026-08-25T09:00:00Z",
      "window_end": "2026-09-01T09:00:00Z",
      "window_days": 7,
      "minimum_volume": 100,
      "risk_threshold": 50,
      "weight_bcs": 0.5,
      "weight_risk_load": 0.5
    },
    "topics": [
      {
        "topic": { "id": "a000...", "name": "Flood Response" },
        "claim_count": 14,
        "above_threshold_count": 6,
        "average_score": 71.2,
        "box_size": 92.5
      }
    ],
    "policies": [
      {
        "rank": 1,
        "policy": {
          "id": "p000...",
          "name": "Jakarta Flood Barrier Programme 2026",
          "source": "cis",
          "ai_policy_id": "ai00...",
          "status": "rolled_out",
          "rolled_out_date": "2026-03-01T00:00:00Z",
          "has_document": true
        },
        "claim_count": 9,
        "above_threshold_count": 5,
        "average_score": 68.4,
        "score": 88.1
      }
    ]
  }
}
```

### O1a — `threshold_ratio` (US67)

Counts **every Existing/Generic claim regardless of review status** —
Unreviewed, Active, Inactive and Action Taken alike. US67 flags this as an open
question; the default chosen here is the one where the number describes the
information environment rather than the team's triage queue. Excluding closed
claims would make the ratio improve every time someone finished a ticket.

A claim with no score counts as **below**, matching `threshold_status` on F3:
escalating on missing data is the one direction that cannot be defended.

### O1b — `sentiment`, the Climate Sentiment Index (PRD 6.6, US68)

```
CSI            = BCS_normalized × 0.5 + (100 − RiskLoad) × 0.5
BCS            = (positive − negative) / total          → −1 … +1
BCS_normalized = (BCS + 1) / 2 × 100                    → 0 … 100
RiskLoad       = Σ(FinalClaimScore_i × Volume_i) / total, for claims scoring ≥ 50
```

- **Window** — a 7-day rolling average (PRD 6.6.3), so one viral event cannot
  swing the headline figure.
- **`momentum`** — the same index computed over a window lagged 24 h, giving the
  direction-of-change indicator. `null` when the lagged window is itself below
  the minimum volume.
- **`band`** — `risky` / `watch` / `healthy` for the red/amber/green gauge, split
  into equal thirds. The PRD specifies the banding without cut points; these are
  documented rather than hidden.
- **Higher is healthier.** RiskLoad is inverted because it reads "higher is
  worse"; inverting aligns it with BCS so the gauge has one direction.
- Every component is returned beside the headline number, for the same reason
  PRD 6.5 requires it of claim scores. US68's click-through renders
  `bcs_normalized` and `risk_load` as the two bars.

**`status`** is one of:

| Value | Meaning |
|---|---|
| `ok` | Computed. `score` and the components are populated. |
| `insufficient_data` | Fewer than `minimum_volume` content items in the window. PRD 6.6.3 requires this rather than a falsely calm score from low engagement. |
| `unavailable` | The AI service has not provisioned per-item sentiment yet. See [sql/02_f6_reference_schema.sql](../sql/02_f6_reference_schema.sql). |

`score` is `null` unless `status` is `ok`. `reason` carries a sentence the UI
can show directly. **O1a, O2 and O3 are unaffected by a non-`ok` status** —
they are computed from `claims`, not from the content stream.

### O2 — `topics` (US69)

One box per **Existing/Generic-claim topic**; Synthetic topics are excluded, so
the treemap cannot be dominated by predictions. Returned largest first.

`box_size` is the 0–100 area weight. US69 leaves the formula open and proposes a
default, which is what is implemented and published on every box:

```
box_size = 0.5 × (above_threshold_count / max_above_threshold_count × 100)
         + 0.5 × (average_score          / max_average_score          × 100)
```

Each input is normalised against the largest topic **in the current set**, not
against a fixed ceiling — the count of above-threshold claims has no natural
upper bound, and dividing by one would flatten every rectangle in a quiet week
into the same size. A set where nothing is above threshold contributes zero from
that half rather than dividing by zero, leaving the average score to order the
topics alone.

### O3 — `policies` (US70)

Ranked by `score`, the **same combined metric** that sizes the O2 treemap.

A claim reaches a policy either through `claim_policies` (many-to-many, US12) or
through `claims.policy_id` (US20); both are read and unioned, so a claim linked
both ways is counted once. Only **Existing/Generic** claims are counted, per
US70's "correlated Existing-claims". `policy` uses the
shared policy reference shape and prefers this backend's F2 record where one
shadows the AI policy, so a policy is never named two different things on two
pages.

### `city.partitioned`

`false` means the AI service does not yet tag content with a city, so the F4
selection **labels** this instance rather than filtering it. Surfaced rather
than hidden: a leadership page must not imply a city breakdown the data cannot
support. See [sql/02_f6_reference_schema.sql](../sql/02_f6_reference_schema.sql).

---

## GET /api/v1/overview/topics/:id

The O2 treemap's click-through modal (US69).

```bash
curl "http://localhost:8080/api/v1/overview/topics/a0000000-0000-0000-0000-000000000002" \
  -H "Authorization: Bearer $TOKEN"
```

**200 OK**

```json
{
  "success": true,
  "message": "topic overview",
  "data": {
    "topic": { "id": "a000...", "name": "Flood Response" },
    "claim_count": 14,
    "above_threshold_count": 6,
    "below_threshold_count": 8,
    "above_under_ratio": 0.75,
    "average_score": 71.2,
    "average_score_mom_percent": 8.4,
    "mom_direction": "up",
    "current_month_average": 71.2,
    "previous_month_average": 65.7,
    "threshold": 70
  }
}
```

- **`average_score_mom_percent`** — month-on-month change of the topic's average
  claim score, over the AI service's `claim_score_snapshots` (a row per rescore
  for **every** claim), not the backend's own watchlist-only snapshots. A
  month-on-month figure computed over the watchlist would describe the team's
  attention rather than the topic. `null` when there is not enough history on
  both sides of the comparison, or when the previous average is zero and the
  percentage would be undefined. `mom_direction` is `up` / `down` / `flat` for
  the ▲ green / ▼ red indicator.
- **`above_under_ratio`** — `above / below`, `null` when nothing is below
  threshold and the ratio is undefined. A UI printing "Infinity" beside a risk
  figure is worse than one printing nothing.

**404** when the topic does not exist, or has no Existing claims in the
configured city.
