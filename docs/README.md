# CIS Backend — Documentation

Backend for **CIS (Climate Immune System)**, the platform that gives a city
climate-communications team a scored, auditable repository of climate
misinformation claims.

Implements **PRD v1.5**: **F6** Overview, **F1** Claim Repository Bank (+ the
Section 6 scoring system), **F2** Public Policy Bank, **F3** Alert Page, **F4**
Admin Settings + the detector control panel and the US65 city configuration, and
**F5** Coordinated-Network Detector.

F5's *detection maths* is not here and never will be — Leiden community
detection, MinHash/LSH, multilingual embeddings, perceptual hashing and
ForceAtlas2 are mature in Python and effectively absent in Go. The same split
already governs the Section 6 claim scores: **the AI service computes, this
backend reads, governs and presents.** What lives here is the scheduling, the
scope and suppression rules, the human review workflow, the allowlist, the
report and evidence-bundle generation, and the audit trail.

---

## Start here

| Document | What it covers |
|---|---|
| **[SETUP.md](SETUP.md)** | **Step-by-step: how to spin up the server**, locally and with Docker |
| [ARCHITECTURE.md](ARCHITECTURE.md) | Layering, package map, and the decisions behind them |
| [DATABASE.md](DATABASE.md) | Table ownership — the rule that shaped the whole design |
| [SCORING.md](SCORING.md) | PRD Section 6 as implemented |
| [AI-INTEGRATION.md](AI-INTEGRATION.md) | The contract with the AI service. **Share this with the AI team.** |

## API reference

Markdown, not Swagger. Start with [api/README.md](api/README.md) for the
response envelope, error codes, auth, and shared query parameters.

| Resource | File |
|---|---|
| Health probes | [api/health.md](api/health.md) |
| Authentication | [api/auth.md](api/auth.md) |
| Topics (filter chips) | [api/topics.md](api/topics.md) |
| **F6** Overview | [api/overview.md](api/overview.md) |
| **F1** Claim Repository Bank | [api/claims.md](api/claims.md) |
| **F2** Public Policy Bank | [api/policies.md](api/policies.md) |
| **F3** Alert Page | [api/alerts.md](api/alerts.md) |
| **F4** Admin Settings + utilities | [api/settings.md](api/settings.md) |
| **F5** Coordinated-Network Detector | [api/networks.md](api/networks.md) |
| AI service callbacks | [api/internal.md](api/internal.md) |

## Other files

- [sql/00_ai_reference_schema.sql](sql/00_ai_reference_schema.sql) — the AI
  team's DDL, for bootstrapping a **local** database only. Never executed by the
  app; never run it against shared Supabase.
- [sql/01_f5_reference_schema.sql](sql/01_f5_reference_schema.sql) — the same,
  for F5's detection pipeline tables. Columns marked `BEYOND 10.10` are this
  backend's proposal and still need AI-team sign-off.
- [sql/02_f6_reference_schema.sql](sql/02_f6_reference_schema.sql) — the PRD
  v1.5 AI-side additions: `content_items.sentiment`, `content_items.city`, and
  the `claim_debunk_segments` table. All three are optional; the file documents
  exactly how the backend degrades without each.

---

## The one thing to know

This backend shares its Supabase Postgres with a separately developed AI
service that is already in production.

> **AI-owned tables are read-only. Every table this backend writes is prefixed
> `cis_`.**

That is enforced by a startup guard, not just convention. It is why claim review
status is an overlay table rather than a column update, why F2 policies are
shadowed via `ai_policy_id` instead of sharing the AI's `policies` table, and
why the F3 chart keeps its own score history. [DATABASE.md](DATABASE.md) has
the full picture.

## Feature map

| PRD | Feature | Key endpoints |
|---|---|---|
| F6 | Overview | `GET /overview`, `GET /overview/topics/:id` |
| F1 | Claim Repository Bank | `GET /claims/repository`, `GET /claims`, `GET /claims/:id`, `PUT /claims/:id/status` |
| F2 | Public Policy Bank | `GET /policies`, `POST /policies`, `GET /policies/:id`, `GET /policies/:id/file` |
| F3 | Alert Page | `GET /alerts`, `POST /alerts`, `PATCH /alerts/:claimId/chart`, `GET /alerts/chart`, `GET /alerts/notifications` |
| F4 | Admin Settings | `GET|PUT /settings/alert-threshold`, `GET|PUT /settings/city`, `GET|PUT /settings/detector`, `POST /admin/generate-generic-claim` |
| F5 | Coordinated-Network Detector | `GET /networks`, `GET /networks/:id`, `PUT /networks/:id/status`, `POST /networks/:id/reports`, `GET|POST /admin/allowlist` |
