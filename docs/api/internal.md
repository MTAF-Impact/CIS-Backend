# Internal Routes — AI Service Callbacks

These endpoints are machine-to-machine. They are **not** protected by the
operator JWT.

If both sides configure `INTERNAL_API_KEY`, the route requires a matching
shared secret instead:

```http
X-Internal-Key: <INTERNAL_API_KEY>
```

The key is compared in constant time.

If `INTERNAL_API_KEY` is left unset (the default for this deployment — no
secret is exchanged with the AI service), the route accepts requests with no
`X-Internal-Key` header at all. This is only safe when the backend and AI
service are reachable exclusively over a private/internal network — never
expose these routes to the public internet without a configured key.

| Error | Cause |
|---|---|
| `401 UNAUTHORIZED` | `INTERNAL_API_KEY` is configured, and the header is missing or does not match. |

> Do not expose these routes to browsers, and never ship the key to a client.

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

See [../AI-INTEGRATION.md](../AI-INTEGRATION.md) for the outbound side of this
contract — what the backend sends to the AI service.
