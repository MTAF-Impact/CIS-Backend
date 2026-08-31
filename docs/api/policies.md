# F2 — Public Policy Bank

All routes require a Bearer token.

Policies registered here live in this backend's own `cis_policies` table. The
backend **never writes** the AI service's `policies` table. Once AI matchmaking
runs (US42), the AI service reports back the policy id it used on its side, and
that value is stored as `ai_policy_id` — the join key for resolving correlated
claims. Until then, `ai_policy_id` is `null` and the claim lists are empty.

---

## GET /api/v1/policies

The main policy list (US35).

**Ordering:** by the newest `created_at` among any claim linked to the policy —
*not* the policy's own creation date. Policies with no linked claims fall back
to their own creation date and sort after every policy that has claim activity.

| Query | Default | Notes |
|---|---|---|
| `years` | — | Comma-separated 4-digit years, multi-select (US34) |
| `q` | — | Search by policy name (US38) |
| `status` | — | `rolled_out` or `not_rolled_out` |
| `page`, `limit` | `1`, `20` | |

```bash
curl "http://localhost:8080/api/v1/policies?years=2026,2027&q=congestion" \
  -H "Authorization: Bearer $TOKEN"
```

**200 OK**

```json
{
  "success": true,
  "message": "public policies",
  "data": [
    {
      "id": "b15bb20f-947f-479a-b6c1-38aa3a4bdfd0",
      "name": "Jakarta Congestion Charge 2026",
      "month_year": "January 2026",
      "rolled_out_date": "2026-01-15T00:00:00Z",
      "created_at": "2026-08-30T14:33:42Z",
      "status": "rolled_out",
      "file_name": "policy.pdf",
      "file_mime_type": "application/pdf",
      "file_size_bytes": 47,
      "download_url": "/api/v1/policies/b15bb20f-947f-479a-b6c1-38aa3a4bdfd0/file",
      "processing_status": "completed",
      "is_processing": false,
      "linked_claim_count": 2,
      "last_claim_activity_at": "2026-08-30T14:32:45Z",
      "ai_policy_id": "b0000000-0000-0000-0000-000000000001"
    }
  ],
  "meta": { "page": 1, "limit": 20, "total": 1, "total_pages": 1 }
}
```

`month_year` is pre-formatted for the card so the frontend does not have to
localize a date itself.

### `processing_status` — the "Processing" badge (US42)

| Value | `is_processing` | Meaning |
|---|:-:|---|
| `pending` | ✅ | Queued; the AI call has not started. |
| `processing` | ✅ | Handed to the AI service; awaiting its result. |
| `completed` | ❌ | Matchmaking finished. Claim lists are final. |
| `failed` | ❌ | The AI call failed. See `processing_error`; retry with `/rematch`. |
| `skipped` | ❌ | No AI service configured (`AI_SERVICE_URL` empty). Not an error. |

Show the Gold "Processing" badge while `is_processing` is `true`, and per PRD
§5.6 do not let users act on auto-matched claims until it clears.

---

## GET /api/v1/policies/years

Distinct rolled-out years present in the bank, for the US34 filter chips.
Descending.

```json
{ "success": true, "message": "available policy years", "data": { "years": [2027, 2026] } }
```

---

## POST /api/v1/policies

The "Add Public Policy" modal (US40).

**Content-Type:** `multipart/form-data`

| Part | Type | Rules |
|---|---|---|
| `file` | file | **required** — PDF or Word only (`.pdf`, `.doc`, `.docx`). No size limit. |
| `name` | text | required, 2–500 |
| `rolled_out_date` | text | required, `YYYY-MM-DD` |
| `description` | text | optional, ≤5000 |

```bash
curl -X POST http://localhost:8080/api/v1/policies \
  -H "Authorization: Bearer $TOKEN" \
  -F "file=@congestion-charge.pdf" \
  -F "name=Jakarta Congestion Charge 2026" \
  -F "rolled_out_date=2026-01-15"
```

**201 Created** — returns the policy card shown above.

On success the backend, in order:

1. Validates the format. The **file extension is authoritative** because
   browsers inconsistently label `.doc`/`.docx`; a declared content type is only
   used to reject an obvious mismatch (so `application/octet-stream` is accepted).
2. Streams the document to storage (Supabase bucket, or local disk in dev).
3. Derives `status` automatically from `rolled_out_date` — **US41, never set by
   the user**: on or before today ⇒ `rolled_out`, otherwise `not_rolled_out`.
4. Kicks off AI matchmaking in the background and returns immediately with
   `processing_status: "pending"`. If the DB insert fails, the uploaded document
   is deleted so no orphan is left behind.

**Errors**

- `422 UNPROCESSABLE_ENTITY` — wrong format. The message is written for direct
  display in the modal:
  `unsupported file format ".txt": only PDF and Word (.pdf, .doc, .docx) documents are accepted`
- `400 BAD_REQUEST` — no `file` part.
- `400 VALIDATION_FAILED` — missing/short `name`, or a `rolled_out_date` that is
  not `YYYY-MM-DD`.

> **No size limit** (US40) is implemented as `APP_BODY_LIMIT_BYTES=0`, which
> resolves to ~2 GiB. A literal absence of a limit is not expressible: the HTTP
> server treats "0 or negative" as *use the 4 MB default*, which would reject
> real policy PDFs. Very large uploads still affect transfer time — consider
> revisiting this as an engineering constraint, as the PRD itself suggests.

---

## GET /api/v1/policies/:id

The policy detail page (US39).

Returns the policy card plus `description` and the two correlated claim lists.
**Both lists use the exact same claim card shape as F1**, so the frontend renders
them with the identical component — including score, bell state, status control
for Existing claims, and the US61 `coordinated_network` indicator.

That last one does not come for free on this side. `PolicyService` assembles
these cards itself rather than calling into `ClaimService`, so the F5 lookup is
wired into both. Wiring only the claim service would ship the icon on F1 and
silently drop it here — precisely the policy-specific variant US39 forbids. The
field is documented in [claims.md](claims.md#coordinated_network--the-us61-indicator).

```json
{
  "success": true,
  "message": "policy detail",
  "data": {
    "id": "b15bb20f-947f-479a-b6c1-38aa3a4bdfd0",
    "name": "Jakarta Congestion Charge 2026",
    "status": "rolled_out",
    "processing_status": "completed",
    "is_processing": false,
    "linked_claim_count": 2,
    "ai_policy_id": "b0000000-0000-0000-0000-000000000001",
    "description": null,
    "existing_claims": [
      {
        "id": "c0000000-0000-0000-0000-000000000001",
        "claim_type": "existing",
        "claim_statement": "The congestion charge is a secret tax that will bankrupt small businesses",
        "topic": { "id": "a0000000-0000-0000-0000-000000000001", "name": "Congestion Charge" },
        "review_status": "action_taken",
        "final_claim_score": 68.9,
        "positive_statement_count": 3,
        "negative_statement_count": 2,
        "is_on_alert": true
      }
    ],
    "non_existing_claims": [
      {
        "id": "c0000000-0000-0000-0000-000000000003",
        "claim_type": "non_existing",
        "claim_statement": "The congestion charge revenue will be diverted to foreign contractors",
        "topic": { "id": "a0000000-0000-0000-0000-000000000001", "name": "Congestion Charge" },
        "review_status": "unreviewed"
      }
    ]
  }
}
```

Both lists are empty when `ai_policy_id` is `null` — correlations do not exist
until matchmaking has reported back.

---

## GET /api/v1/policies/:id/file

Downloads the policy document (US37, the card's download icon).

| Query | Default | Behaviour |
|---|---|---|
| `mode` | redirect | `307` redirect to a time-limited signed URL (Supabase). |
| `mode=json` | — | Returns the URL as JSON instead of redirecting. |

With the `local` storage driver there is no signed URL, so by default the
endpoint streams the bytes directly with `Content-Disposition: attachment`.
**Under the `local` driver, `mode=json` does not 404 or stream bytes** — it
returns the same JSON shape as the `supabase` driver, except `url` is this same
`/api/v1/policies/:id/file` path (not a signed URL) and `is_signed_url` is
`false`; the caller must re-request this endpoint without `mode=json` to get
the bytes.

```bash
curl -L -o policy.pdf "http://localhost:8080/api/v1/policies/$ID/file" \
  -H "Authorization: Bearer $TOKEN"
```

`mode=json` response:

```json
{
  "success": true,
  "message": "policy document",
  "data": {
    "file_name": "policy.pdf",
    "mime_type": "application/pdf",
    "size_bytes": 47,
    "url": "https://<project>.supabase.co/storage/v1/object/sign/...",
    "expires_at": "2026-08-30T15:33:59Z",
    "is_signed_url": true
  }
}
```

**Errors** — `404 NOT_FOUND` unknown policy or no attached document.

---

## GET /api/v1/policies/:id/processing

Lightweight endpoint for polling the "Processing" badge (US42).

```json
{
  "success": true,
  "message": "matchmaking status",
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

Poll while `is_processing` is `true`; every 3–5 seconds is plenty.

A badge that never clears is recovered automatically. `processing` is otherwise
a terminal state on this side — only the AI service's callback moves a policy out
of it, and that callback is best-effort and never retried on its end. So a
background sweep (`CRON_MATCHMAKING_RETRY_SPEC`, every 15 minutes) re-queues any
policy that has been `processing` for longer than `AI_MATCHMAKING_STALE_AFTER`
(default 30 minutes), alongside anything `pending` or `failed`. `attempts` bounds
that at 3 tries, after which a manual `/rematch` is needed.

---

## POST /api/v1/policies/:id/rematch

Re-queues AI matchmaking, resetting the attempt counter. Use after a `failed`
status.

This sends `force: true` to the AI service, asking it to genuinely re-run the
pipeline rather than re-report the previous run's counts — which for a failed
run are typically `0, 0`. See [AI-INTEGRATION.md](../AI-INTEGRATION.md#force--please-honour-this):
until the AI service honours the flag, a failed matchmaking cannot recover, and
this button reports `completed` with the failed run's numbers.

**200 OK** — same body as `/processing`.

**Errors** — `409 CONFLICT` already running · `503 SERVICE_UNAVAILABLE` no
`AI_SERVICE_URL` configured · `404 NOT_FOUND`.

---

## PATCH /api/v1/policies/:id

Edits policy metadata. All fields optional; at least one required.

| Field | Rules |
|---|---|
| `name` | 2–500 |
| `rolled_out_date` | `YYYY-MM-DD` — **re-derives `status`** (US41) |
| `description` | ≤5000 |

**Errors** — `400 BAD_REQUEST` if no updatable field was supplied.

---

## PUT /api/v1/policies/:id/file

Replaces a policy's document in place — the id, `ai_policy_id`, and every
existing claim correlation are preserved, unlike `DELETE` + re-create.

**Content-Type:** `multipart/form-data`

| Part | Type | Rules |
|---|---|---|
| `file` | file | **required** — PDF or Word only (`.pdf`, `.doc`, `.docx`). Same validation as `POST /policies`. |

```bash
curl -X PUT "http://localhost:8080/api/v1/policies/$ID/file" \
  -H "Authorization: Bearer $TOKEN" \
  -F "file=@congestion-charge-v2.pdf"
```

**200 OK** — returns the updated policy card (same shape as `POST /policies`).

On success the backend, in order:

1. Validates and stores the new document (old file is deleted once the DB
   record points at the new one; if the DB update fails, the newly-uploaded
   file is deleted instead so nothing is orphaned either way).
2. If `AI_SERVICE_URL` is configured, resets `processing_status` to `pending`
   and re-queues AI matchmaking against the new document with `force: true`, the
   same way `/rematch` does — so correlations catch up with the new file.
   Existing correlations are left as-is until the new job reports back.

   The `force` flag carries the same caveat as `/rematch`: until the AI service
   honours it, the replaced document is never read and correlations stay pinned
   to the superseded file.

**Errors** — `404 NOT_FOUND` unknown policy · `422 UNPROCESSABLE_ENTITY` wrong
file format · `400 BAD_REQUEST` no `file` part · `409 CONFLICT` matchmaking is
already running for this policy.

---

## DELETE /api/v1/policies/:id

Deletes the policy record and its stored document.

The database row is removed first; if the storage deletion then fails, the error
is logged and the request still succeeds. That ordering leaves a recoverable
orphaned file rather than a record pointing at nothing.

**200 OK** — `{"success": true, "message": "public policy deleted"}`

> Claims the AI service linked to this policy are **not** deleted — they live in
> AI-owned tables that this backend never writes.
