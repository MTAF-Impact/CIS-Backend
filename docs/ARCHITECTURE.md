# Architecture

Go 1.25 · Fiber v2 · GORM · Supabase Postgres + Storage.

---

## Layering

```
HTTP request
    │
    ▼
middleware/     request id → recover → logger → CORS → auth
    │
    ▼
handler/        parse + validate input, shape the response
    │           (never touches GORM)
    ▼
service/        business logic, PRD rules, cross-repository orchestration
    │           (returns apperr, knows nothing about HTTP)
    ▼
repository/     every SQL query lives here
    │
    ▼
GORM → Postgres
```

The rules that keep this honest:

- **Handlers never import GORM.** They call a service and return a
  `response.OK/Created/List`.
- **Services never import Fiber.** They take a `context.Context` and return
  domain errors from `internal/pkg/apperr`, which the error handler maps to
  status codes. This keeps the PRD logic testable without spinning up HTTP.
- **Only repositories build queries.** That is what makes the AI-table
  read-only guarantee auditable — there is exactly one place to check.

## Package map

| Package | Responsibility |
|---|---|
| `cmd/api` | Startup: config → DB → migrate → dependency graph → cron → serve, with graceful shutdown |
| `internal/config` | Typed env loading and validation. Fails fast on missing secrets. |
| `internal/database` | GORM connection, and the migrator that enforces the `cis_` ownership boundary |
| `internal/models` | GORM structs, split into AI-owned (read-only) and `cis_*` (owned) |
| `internal/dto` | Request/response shapes + validation |
| `internal/repository` | All SQL |
| `internal/service` | Business logic |
| `internal/handler` | Fiber handlers |
| `internal/middleware` | Error translation, logging, CORS, JWT auth |
| `internal/router` | The full route table — one place to see the API surface |
| `internal/storage` | File-store interface + Supabase and local drivers |
| `internal/aiclient` | Outbound HTTP to the AI service |
| `internal/scheduler` | Cron jobs |
| `internal/scoring` | PRD Section 6 presentation contract: weights, clamps, dormancy |
| `internal/pkg/apperr` | Typed domain errors |
| `internal/pkg/response` | The single JSON envelope |

DTOs exist so the AI service's column layout is never leaked directly to
clients, and the API stays stable if that layout changes.

---

## The constraint that shaped everything

The database is shared with a separately developed AI service that is already in
production. The backend treats AI tables as **read-only** and writes exclusively
to `cis_`-prefixed tables. This is enforced by a startup guard, not just
convention — see [DATABASE.md](DATABASE.md).

Three consequences worth understanding:

**1. Claim status is an overlay.** `PUT /claims/:id/status` writes
`cis_claim_reviews`, and reads resolve
`COALESCE(cis_claim_reviews.status, 'unreviewed')` via a `LEFT JOIN`. The AI's
own `claims.status` is untouched, so neither side can clobber the other. Status
filtering happens in SQL, so pagination stays correct.

**2. Policies are shadowed, not shared.** F2 policies live in `cis_policies`
with a nullable `ai_policy_id` soft reference the AI service fills in after
matchmaking. Every claim↔policy correlation joins through that column.

**3. Score history is derived.** The AI service stores only a claim's current
score, but F3 charts it over time, so a cron job snapshots watched claims into
`cis_claim_score_snapshots`.

---

## Request lifecycle

Every response uses one envelope (`internal/pkg/response`). Services return
`*apperr.Error` carrying an HTTP status and a stable code; the Fiber error
handler renders it and logs 5xx server-side. With `APP_ENV=production`,
unexpected internal errors return a generic message while the detail is logged.

Auth middleware is attached **per route group**, not as a blanket `Use` on
`/api/v1`, so which routes are protected does not depend on registration order.
`internal/router/router_test.go` dispatches a real anonymous request at every
protected route and asserts `401`.

---

## Notable decisions

**Fiber v2, not v3.** v3 is newer; v2 is what the ecosystem and documentation
have settled on.

**GORM AutoMigrate over a migration tool.** The PRD asks for GORM migrations,
and the surface is small: 7 owned tables, additive changes only. AutoMigrate
never drops columns, which is the risk that would otherwise matter on a shared
database.

**UUIDs generated in Go.** The AI schema declares no column defaults, so ids are
assigned in `BeforeCreate` hooks to match.

**Refresh tokens hashed, single-use.** Only a SHA-256 hash is stored. A plain
hash is right here — unlike passwords, these are 256-bit random values and are
not guessable, so no work factor is needed. Rotation revokes the presented token.

**No roles.** Per the chosen scope: any authenticated user can call every
endpoint, including F4. Adding roles later means one column and one middleware.

**One combined `/claims/repository` endpoint.** The F1 page needs both sections
plus the last-fetched timestamp; serving them together avoids three round trips
on page load and keeps the US1 "both sections always visible" rule in one place.

**Batched card queries.** Rendering a list of cards needs statement counts and
alert membership per claim. Both are fetched in a single grouped query keyed by
claim id, so a page of cards costs a constant number of queries rather than N+1.

**No client-side timeout on uploads.** US40 allows arbitrarily large policy
documents; a fixed HTTP client deadline would truncate a slow large upload.
Cancellation is driven by request context instead.

---

## Background jobs

`internal/scheduler`, using `robfig/cron/v3` in UTC.

| Job | Default schedule | Purpose |
|---|---|---|
| Policy rollout | `0 1 * * *` | Flip `not_rolled_out → rolled_out` once the date passes (US41), and retry stuck matchmaking |
| Score snapshot | `0 * * * *` | Capture watched claims' scores for the F3 chart; prune beyond ~400 days |

The rollout job also runs once at boot, so a server that was down over a
scheduled window catches up instead of waiting for the next tick.

Both are exposed as manual endpoints for demos
(`POST /api/v1/admin/snapshot-scores`).

> Run cron on **one** instance. If you scale out, set `CRON_ENABLED=false` on the
> others.

---

## Testing

```bash
go test ./...
```

- `internal/models` — claim-type alias normalization, US41 rollout derivation
  (including the "today is inclusive" boundary), and rejection of the retired
  `debunk`/`prebunk` statuses
- `internal/storage` — US40 format allowlist, and that a traversal filename is
  contained inside the storage root
- `internal/router` — every documented route is registered, literal paths are
  not shadowed by `:id` siblings, and **every protected route returns 401 to an
  anonymous request** (dispatched, not inferred)

Repository and service layers are covered by the end-to-end verification
described in [SETUP.md](SETUP.md), run against a real Postgres with pgvector.
