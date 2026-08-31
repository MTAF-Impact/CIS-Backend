# API Conventions

Base URL: `http://localhost:8080` (local) · All feature routes live under `/api/v1`.

**Exception:** the two [health probes](health.md), `GET /health` and
`GET /health/ready`, are mounted at the server root, *not* under `/api/v1` —
concatenating the prefix onto them 404s.

Documentation is intentionally Markdown, not Swagger/OpenAPI.

| Resource | File |
|---|---|
| Health probes | [health.md](health.md) |
| Authentication | [auth.md](auth.md) |
| Topics (filter chips) | [topics.md](topics.md) |
| **F1** Claim Repository Bank | [claims.md](claims.md) |
| **F2** Public Policy Bank | [policies.md](policies.md) |
| **F3** Alert Page | [alerts.md](alerts.md) |
| **F4** Admin Settings + utilities | [settings.md](settings.md) |
| AI service callbacks | [internal.md](internal.md) |

---

## Response envelope

Every endpoint — success or failure — returns the same shape.

**Success**

```json
{
  "success": true,
  "message": "claim detail",
  "data": { }
}
```

**List** (adds `meta`)

```json
{
  "success": true,
  "message": "claims",
  "data": [],
  "meta": { "page": 1, "limit": 20, "total": 42, "total_pages": 3 }
}
```

**Error**

```json
{
  "success": false,
  "message": "claim not found",
  "error": { "code": "NOT_FOUND" }
}
```

Validation failures add per-field `details`:

```json
{
  "success": false,
  "message": "request validation failed",
  "error": {
    "code": "VALIDATION_FAILED",
    "details": { "email": "must be a valid email address" }
  }
}
```

## Error codes

| HTTP | `error.code` | Meaning |
|---|---|---|
| 400 | `BAD_REQUEST` | Malformed input — bad UUID, unparseable body, invalid query value. |
| 400 | `VALIDATION_FAILED` | Body failed field validation. See `error.details`. |
| 401 | `UNAUTHORIZED` | Missing, malformed, or expired access token. |
| 403 | `FORBIDDEN` | Registration disabled, or internal routes not configured. |
| 404 | `NOT_FOUND` | Resource does not exist. |
| 409 | `CONFLICT` | Email already taken, matchmaking already running. |
| 413 | `PAYLOAD_TOO_LARGE` | Upload exceeded `APP_BODY_LIMIT_BYTES`. |
| 422 | `UNPROCESSABLE_ENTITY` | Parsed fine but semantically invalid — bad file format, out-of-range threshold, Synthetic claim sent to F3. |
| 500 | `INTERNAL_ERROR` | Unexpected server fault. Detail is logged, not returned, when `APP_ENV=production`. |
| 503 | `SERVICE_UNAVAILABLE` | A dependency is unreachable or unconfigured (AI service, database). |

## Authentication

All `/api/v1` routes require a Bearer access token except `POST /auth/register`,
`POST /auth/login`, `POST /auth/refresh`, and the `/health` probes.

```http
Authorization: Bearer <access_token>
```

There are **no roles**: any authenticated user may call every endpoint,
including the F4 admin settings.

The `/api/v1/internal/*` routes are machine-to-machine callbacks from the AI
service and do not take an operator Bearer token; see [internal.md](internal.md).

## Pagination

| Param | Default | Notes |
|---|---|---|
| `page` | `1` | 1-indexed. |
| `limit` | `20` | Clamped to a maximum of `200`. |

Out-of-range values are silently clamped rather than rejected.

## Shared query parameters

| Param | Applies to | Format |
|---|---|---|
| `status` | claims | `all` (default), `unreviewed`, `active`, `inactive`, `action_taken` |
| `topic_ids` | claims | Comma-separated UUIDs. `all` is ignored. Multi-select per US6/US15. |
| `type` | claims | `existing`, `non_existing`, `all`. Aliases `generic` / `synthetic` accepted. |
| `q` | claims, policies, alerts | Free-text search. `%` and `_` are escaped, so they match literally. |
| `years` | policies | Comma-separated 4-digit years (US34). |
| `granularity` | charts | `day`, `week` (default), `month`, `year`. |

## Timestamps and scores

- All timestamps are RFC 3339. The server operates in UTC.
- All PRD Section 6 scores are on a fixed **0–100** scale, except `npr` (0–1)
  and `discount_factor` (0.5–1). Values are clamped defensively on read.
- A `null` score means the AI service has not computed it yet — not zero.
