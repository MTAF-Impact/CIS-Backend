# Topics

All routes require a Bearer token.

Topics back the multi-select filter chips on both F1 sections (US6 for S1,
US15 for S2). Every claim belongs to exactly one topic (US3).

Topics are **owned and written by the AI service** — including new topics it
creates during policy matchmaking (US42). This backend reads them only, so there
are no create, update, or delete endpoints.

---

## GET /api/v1/topics

Every topic, alphabetical, annotated with per-type claim counts so the chips can
show how much is behind each filter.

```bash
curl http://localhost:8080/api/v1/topics -H "Authorization: Bearer $TOKEN"
```

**200 OK**

```json
{
  "success": true,
  "message": "topics",
  "data": [
    {
      "id": "a0000000-0000-0000-0000-000000000001",
      "name": "Congestion Charge",
      "description": "Road pricing policy",
      "existing_claim_count": 1,
      "non_existing_claim_count": 1
    },
    {
      "id": "a0000000-0000-0000-0000-000000000002",
      "name": "Flood Response",
      "description": "Flooding and drainage",
      "existing_claim_count": 1,
      "non_existing_claim_count": 0
    }
  ]
}
```

Feed the `id` values into the `topic_ids` query parameter on
[claims.md](claims.md) endpoints. Multi-select is comma-separated:

```
GET /api/v1/claims/repository?topic_ids=a0000000-...-0001,a0000000-...-0002
```

When two or more topics are selected, ranking is computed over the **merged
pool** of claims from all of them — not top-10-per-topic (US7, US16).

An empty `topic_ids`, or the literal value `all`, means no topic filtering.

---

## GET /api/v1/topics/:id

A single topic with its claim counts.

```bash
curl "http://localhost:8080/api/v1/topics/a0000000-0000-0000-0000-000000000001" \
  -H "Authorization: Bearer $TOKEN"
```

**200 OK**

```json
{
  "success": true,
  "message": "topic",
  "data": {
    "id": "a0000000-0000-0000-0000-000000000001",
    "name": "Congestion Charge",
    "description": "Road pricing policy",
    "existing_claim_count": 1,
    "non_existing_claim_count": 1
  }
}
```

**Errors** — `400 BAD_REQUEST` malformed UUID · `404 NOT_FOUND` unknown topic.
