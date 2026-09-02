# Internal Routes — AI Service Callbacks

These endpoints are machine-to-machine callbacks from the AI service. They do
not take an operator JWT — send the request with `Content-Type` and the body.

They belong to the private integration between the backend and the AI service
and are not part of the operator-facing API. Deploy them so they are reachable
only from the AI service, for example by restricting the `/api/v1/internal/`
prefix at the ingress/load balancer or binding it to an internal-only listener.

---

## POST /api/v1/internal/policies/:id/matchmaking-result

Reports the outcome of the US42 matchmaking pipeline for a policy.

`:id` is the **`cis_policies.id`** the backend sent in its matchmaking request —
not the AI service's own policy id.

**Body**

| Field | Type | Rules |
|---|---|---|
| `status` | string | required — `completed`, `failed`, or `processing` |
| `ai_policy_id` | string | optional UUID — the id used in the AI service's own `policies` table |
| `matched_claim_count` | int | optional, ≥0 — informational |
| `generated_claim_count` | int | optional, ≥0 — informational |
| `error` | string | optional, ≤2000 — required in spirit when `status` is `failed` |

```bash
curl -X POST \
  "http://localhost:8080/api/v1/internal/policies/b15bb20f-947f-479a-b6c1-38aa3a4bdfd0/matchmaking-result" \
  -H 'Content-Type: application/json' \
  -d '{
    "ai_policy_id": "b0000000-0000-0000-0000-000000000001",
    "status": "completed",
    "matched_claim_count": 1,
    "generated_claim_count": 1
  }'
```

**200 OK**

```json
{
  "success": true,
  "message": "matchmaking result recorded",
  "data": {
    "policy_id": "b15bb20f-947f-479a-b6c1-38aa3a4bdfd0",
    "processing_status": "completed",
    "is_processing": false,
    "attempts": 1,
    "processed_at": "2026-08-30T14:33:58Z",
    "ai_policy_id": "b0000000-0000-0000-0000-000000000001",
    "linked_claim_count": 2
  }
}
```

### Why `ai_policy_id` matters

This is the single most important field in the integration.

The backend never inserts into the AI service's `policies` table, so a policy
uploaded through F2 has no identity on the AI side until the AI service creates
one and reports it here. `ai_policy_id` is stored on `cis_policies` as a soft
reference (no foreign key) and is the join key for **every** claim-to-policy
correlation:

- `claim_policies.policy_id = ai_policy_id` → Existing claims (many-to-many, US12/US39)
- `claims.policy_id = ai_policy_id` → Synthetic claims (one-to-many, US20/US39)

**Until this callback supplies it, `GET /api/v1/policies/:id` returns empty
claim lists** and the card keeps showing the "Processing" badge, no matter how
many rows the AI service has written on its side.

### Status handling

| `status` | Effect |
|---|---|
| `completed` | `processing_status = completed`, `processed_at` set, error cleared. Badge clears; claim lists go live. |
| `failed` | `processing_status = failed`, `error` stored. Badge clears; the UI can offer `POST /policies/:id/rematch`. |
| `processing` | Stays in progress. Send this to acknowledge a long job and keep the badge on. |

Sending `ai_policy_id` alongside `status: "processing"` is valid and encouraged —
it lets correlations resolve as soon as they are written, before the job as a
whole finishes.

**Errors** — `404 NOT_FOUND` unknown policy · `422 UNPROCESSABLE_ENTITY`
malformed `ai_policy_id` · `400 VALIDATION_FAILED` invalid `status`.

---

## GET /api/v1/internal/detection/exclusions

The detector's two exclusion lists, read by the pipeline before candidate
selection (PRD 10.5.1, 10.5.2.2).

**This is the one place the read direction *could* reverse.** Everywhere else the
backend reads AI-owned tables; here the AI service would read a backend-owned
one. The declared-coordination allowlist and the common-phrase list record human
decisions — which is why they live in `cis_*` tables — but they are inputs to
the pipeline, not outputs of it.

In practice it does not reverse: the pipeline reads both lists off the Flow 7
request body and **nothing calls this route today**. That is what lets the
ownership rule hold with no exception in either direction. The route stays
because it costs nothing, and because it is the surface that makes the split
legible.

```bash
curl http://localhost:8080/api/v1/internal/detection/exclusions
```

**200 OK**

```json
{
  "success": true,
  "message": "detector exclusion lists",
  "data": {
    "accounts": [
      { "platform": "x", "platform_account_id": "1428...", "handle": "@jakartaflood_ngo" }
    ],
    "phrases": ["#JakartaBanjir", "lapor pak gubernur"]
  }
}
```

`accounts` are keyed on `(platform, platform_account_id)`, **not** on the
handle. Handles get renamed; the platform-issued id does not, so protection
keyed on the handle alone would lapse the moment an NGO rebranded. Removed
entries are excluded — only active ones are returned.

`phrases` are slogans, hashtags and civic boilerplate that must not count as
content duplication. Without them a shared campaign hashtag reads as
coordination, which is exactly how a detector accuses a genuine campaign.

**Pulling this is optional, and currently unused.** The backend also *sends* both
lists inside every detection-run request (Flow 7), and the AI pipeline reads them
from there. This route remains available for a pipeline that would rather pull
them at its own cadence.

---

See [../AI-INTEGRATION.md](../AI-INTEGRATION.md) for the outbound side of this
contract — what the backend sends to the AI service.
