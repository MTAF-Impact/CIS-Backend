# CIS Backend

Backend API for **CIS (Climate Immune System)** — an AI-powered platform giving
a city's climate communications team a structured "immune system" for its
information environment: detecting and scoring emerging misinformation claims,
linking them to public policy, and surfacing ready-to-use response content.

Go 1.25 · Fiber v2 · GORM · Supabase (Postgres + Storage)

Implements PRD v1.3 Phase 1 — **F1** Claim Repository Bank (with the Section 6
Claim Scoring System), **F2** Public Policy Bank, **F3** Alert Page, and **F4**
Admin Settings. F5 is out of scope for this version.

---

## Quick start

```bash
go mod download
cp .env.example .env      # fill in DATABASE_URL, JWT_SECRET, SUPABASE_*
go run ./cmd/api
```

```bash
curl http://localhost:8080/health/ready
```

Full walkthrough, including Docker and troubleshooting:
**[docs/SETUP.md](docs/SETUP.md)**

---

## Documentation

All endpoints are documented in Markdown (no Swagger) under
[`docs/api/`](docs/api/README.md).

| | |
|---|---|
| [docs/SETUP.md](docs/SETUP.md) | **How to spin up the server** — local and Docker |
| [docs/api/README.md](docs/api/README.md) | API conventions, envelope, error codes |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | Layering and design decisions |
| [docs/DATABASE.md](docs/DATABASE.md) | Table ownership and schema |
| [docs/SCORING.md](docs/SCORING.md) | PRD Section 6 as implemented |
| [docs/AI-INTEGRATION.md](docs/AI-INTEGRATION.md) | Contract with the AI service |

---

## Shared database

This backend shares its Supabase Postgres with a **separately developed AI
service already in production**. The design follows one rule:

> **AI-owned tables (`claims`, `topics`, `policies`, `content_items`, …) are
> read-only. Every table this backend writes is prefixed `cis_`.**

It is enforced at startup, not just documented — the migrator refuses to run
against any table without the `cis_` prefix. Five AI tables carry pgvector
`embedding` columns that Go cannot represent, so migrating them would silently
destroy the AI service's semantic search.

Consequences that show up throughout the code:

- **Claim review status** is an overlay table (`cis_claim_reviews`), so an AI
  pipeline re-run can never overwrite a human decision — and vice versa.
- **F2 policies** live in `cis_policies` and reference the AI's policy id via a
  soft `ai_policy_id` link supplied by the matchmaking callback.
- **F3 score history** is snapshotted into `cis_claim_score_snapshots`, since
  the AI service stores only a claim's current score.

See [docs/DATABASE.md](docs/DATABASE.md).

---

## Project layout

```
cmd/api/              entrypoint
internal/
  config/             env loading and validation
  database/           GORM connection + the ownership-enforcing migrator
  models/             AI-owned (read-only) and cis_* (owned) structs
  dto/                request/response shapes + validation
  repository/         all SQL
  service/            business logic
  handler/            Fiber handlers
  middleware/         errors, logging, CORS, JWT, internal API key
  router/             the full route table
  storage/            Supabase + local file drivers
  aiclient/           outbound calls to the AI service
  scheduler/          cron jobs
  scoring/            PRD Section 6 presentation contract
docs/                 documentation (see above)
```

---

## Commands

```bash
make run          # go run ./cmd/api
make build        # compile to ./bin
make test         # go test ./...
make lint         # go vet + gofmt check
make docker-up    # docker compose up --build -d
make docker-down  # docker compose down
```

---

## Configuration

Every credential comes from the environment; nothing is hardcoded. `.env` is
gitignored — see [`.env.example`](.env.example) for the full annotated list.

Minimum to boot:

| Variable | Purpose |
|---|---|
| `DATABASE_URL` | Supabase Postgres connection string |
| `JWT_SECRET` | Signing key (32+ chars required in production) |
| `SUPABASE_URL`, `SUPABASE_SERVICE_ROLE_KEY` | Policy document storage |

Optional: `AI_SERVICE_URL` and `BACKEND_PUBLIC_URL`. Without the first the
backend runs normally — policy matchmaking records `skipped`, and every `/admin`
endpoint that proxies onto the AI service returns `503`.

The AI service needs `BACKEND_URL` pointed back here, or its Flow 2 callbacks
never arrive. See [docs/AI-INTEGRATION.md](docs/AI-INTEGRATION.md) for the whole
contract and [docs/SETUP.md](docs/SETUP.md#the-ai-services-side-of-the-deployment)
for the deployment checklist.
