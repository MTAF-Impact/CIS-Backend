# Setup — Running the Server

Two paths: **locally with `go run`** (day-to-day development) and **Docker**
(deployment). Local runs do not need Docker.

---

## Prerequisites

| Requirement | Version | Notes |
|---|---|---|
| Go | 1.25+ | `go version`. Go 1.21–1.24 also work: `go.mod` pins 1.25 and the default `GOTOOLCHAIN=auto` downloads it on first build. |
| Supabase project | — | Postgres + Storage |
| Docker | 20.10+ | Only for the Docker path |
| AI service | — | Optional; the backend runs without it |

---

# Part 1 — Run locally (no Docker)

## Step 1 — Get the code and dependencies

```bash
cd CIS-Backend
go mod download
```

## Step 2 — Create the Supabase Storage bucket

Policy documents (US40) are stored in Supabase Storage.

1. Supabase dashboard → **Storage** → **New bucket**
2. Name it `policy-documents`
3. Leave it **private** — the backend serves files through time-limited signed
   URLs, so the bucket must not be public
4. Create

## Step 3 — Collect your credentials

**Database** — Project Settings → **Database** → Connection string → URI.

Two pooler ports are offered, and the choice matters:

| Port | Mode | Use |
|---|---|---|
| `5432` | Session | **Recommended.** Works with default settings. |
| `6543` | Transaction | Also fine, but you **must** set `DB_PREFER_SIMPLE_PROTOCOL=true` — this pooler cannot handle the prepared statements the driver uses by default. |

**Storage** — Project Settings → **API**:
- Project URL → `SUPABASE_URL`
- `service_role` secret → `SUPABASE_SERVICE_ROLE_KEY`

> The `service_role` key bypasses row-level security. Keep it server-side only,
> never in a frontend bundle.

## Step 4 — Configure the environment

```bash
cp .env.example .env
```

Generate a JWT secret:

```bash
openssl rand -base64 48
```

Edit `.env` — at minimum:

```dotenv
DATABASE_URL=postgresql://postgres.<ref>:<password>@aws-0-<region>.pooler.supabase.com:5432/postgres
JWT_SECRET=<the value you just generated>

SUPABASE_URL=https://<ref>.supabase.co
SUPABASE_SERVICE_ROLE_KEY=<service_role key>
SUPABASE_STORAGE_BUCKET=policy-documents

# Creates your first login on boot
SEED_USER_EMAIL=admin@yourcity.go.id
SEED_USER_PASSWORD=ChangeMe123!
SEED_USER_NAME=CIS Admin

# Optional — leave empty to run without the AI service
AI_SERVICE_URL=
INTERNAL_API_KEY=
```

`.env` is gitignored. **Never commit it.**

## Step 5 — Run

```bash
go run ./cmd/api
```

Expected output:

```
[boot] CIS Backend starting in development mode
[boot] connected to Postgres
[migrate] migrated 7 backend-owned tables (cis_*); AI-owned tables untouched
[migrate] seeded initial user admin@yourcity.go.id
[boot] storage driver: supabase
[boot] listening on http://localhost:8080
```

That third line is the one to check: only `cis_*` tables are ever migrated. See
[DATABASE.md](DATABASE.md).

If you see `WARNING: AI-owned tables not found`, the AI service has not
provisioned its schema yet. The server still runs; claim and topic endpoints
return empty results.

## Step 6 — Verify

```bash
curl http://localhost:8080/health
curl http://localhost:8080/health/ready
```

`/health/ready` should report `"database": "up"`.

## Step 7 — Log in

```bash
curl -X POST http://localhost:8080/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"admin@yourcity.go.id","password":"ChangeMe123!"}'
```

Copy `access_token` and use it:

```bash
TOKEN="<access_token>"
curl http://localhost:8080/api/v1/claims/repository -H "Authorization: Bearer $TOKEN"
```

Full endpoint reference: [api/README.md](api/README.md).

---

## Optional — a fully local database

To develop without touching the shared Supabase database:

```bash
# 1. Postgres with pgvector, matching what the AI service needs
docker run -d --name cis-pg \
  -e POSTGRES_PASSWORD=localpass -e POSTGRES_DB=cis \
  -p 5432:5432 pgvector/pgvector:pg16

# 2. Load the AI team's schema so claim endpoints have tables to read
docker exec -i cis-pg psql -U postgres -d cis -f - < docs/sql/00_ai_reference_schema.sql
```

Then point `.env` at it and switch storage to disk:

```dotenv
DATABASE_URL=postgresql://postgres:localpass@localhost:5432/cis?sslmode=disable
STORAGE_DRIVER=local
STORAGE_LOCAL_DIR=./uploads
```

> `00_ai_reference_schema.sql` is a convenience copy for local bootstrapping
> only. **Never run it against the shared Supabase database** — the AI team owns
> those tables there.

---

# Part 2 — Run with Docker

For deployment. The image is multi-stage and runs as a non-root user.

## Step 1 — Configure

```bash
cp .env.example .env
# fill in the same values as Step 4 above
```

## Step 2 — Build and start

```bash
docker compose up --build -d
```

Or with plain Docker:

```bash
docker build -t cis-backend:latest .
docker run -d --name cis-backend -p 8080:8080 --env-file .env cis-backend:latest
```

## Step 3 — Verify

```bash
docker compose ps
docker compose logs -f api
curl http://localhost:8080/health/ready
```

## Step 4 — Manage

```bash
docker compose logs -f api     # follow logs
docker compose restart api     # restart
docker compose down            # stop and remove
docker compose up --build -d   # rebuild after code changes
```

> With `STORAGE_DRIVER=local`, uploads land in a named volume so they survive
> container restarts. For any real deployment use `STORAGE_DRIVER=supabase`:
> local disk does not survive a redeploy and does not scale past one instance.

---

## Deployment notes

Set these for production:

```dotenv
APP_ENV=production
JWT_SECRET=<32+ characters — startup fails otherwise>
AUTH_ALLOW_REGISTRATION=false
CORS_ALLOWED_ORIGINS=https://cis.yourcity.go.id
STORAGE_DRIVER=supabase
DB_LOG_LEVEL=error
INTERNAL_API_KEY=<strong random secret>
```

- `APP_ENV=production` stops internal error details being returned to clients.
- A wildcard CORS origin disables credentialed requests; list real origins.
- Point liveness at `/health` and readiness at `/health/ready`. `/health` does
  no dependency checks, so a database blip will not restart a healthy container.
- Run **one** instance with `CRON_ENABLED=true`. If you scale out, set
  `CRON_ENABLED=false` on the others so the rollout and snapshot jobs do not run
  concurrently.

---

## Common problems

**`missing required environment variables: ...`**
Config validation refused to start with an incomplete setup. The message lists
exactly what is missing.

**`JWT_SECRET must be at least 32 characters in production`**
Generate one with `openssl rand -base64 48`.

**`ping database: ... SASL auth failed` / password errors**
Wrong password, or the password contains characters that must be percent-encoded
in a URI (`@` → `%40`, `#` → `%23`). Use the discrete `DB_*` variables instead
if escaping is fiddly.

**`prepared statement "stmtcache_..." already exists`**
You are on the transaction pooler (port 6543). Set
`DB_PREFER_SIMPLE_PROTOCOL=true`, or move to port 5432.

**`WARNING: AI-owned tables not found`**
Expected on a database where the AI pipeline has not run. Load
`docs/sql/00_ai_reference_schema.sql` locally, or point at the shared database.

**Policy upload returns 422**
Only `.pdf`, `.doc`, and `.docx` are accepted (US40). The response message is
written for direct display in the upload modal.

**Policy card stuck on "Processing"**
Matchmaking is waiting on the AI service. Check
`GET /api/v1/policies/:id/processing`. With `AI_SERVICE_URL` empty the status is
`skipped`, not `pending`, so the badge clears immediately. After a `failed`
status, retry with `POST /api/v1/policies/:id/rematch`.

**F3 chart is empty**
Three possible causes, in order of likelihood: no claim has its chart checkbox
ticked (US28 — the default); the watched claims have no snapshots yet (run
`POST /api/v1/admin/snapshot-scores`); or the `from`/`to` window excludes them.

---

## Useful commands

```bash
go run ./cmd/api          # run
go build ./...            # compile
go vet ./...              # static analysis
go test ./...             # tests
gofmt -l .                # list unformatted files
```

A `Makefile` wraps these: `make run`, `make build`, `make test`, `make lint`,
`make docker-up`, `make docker-down`.
