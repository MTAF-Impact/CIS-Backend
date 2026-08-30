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

**Errors** — `400 VALIDATION_FAILED` / `422 UNPROCESSABLE_ENTITY` outside 0–100.

---

## POST /api/v1/admin/generate-generic-claim

The "Generate Generic Claim" MVP test-data button (US33).

**Body** (optional)

| Field | Type | Rules |
|---|---|---|
| `topic_id` | string | optional UUID — pins the claim to an existing topic |

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
> draft — and returns its id.

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
