# CIS Backend — Documentation

Backend for **CIS (Climate Immune System)**, the platform that gives a city
climate-communications team a scored, auditable repository of climate
misinformation claims.

Implements PRD v1.3 Phase 1: **F1** Claim Repository Bank (+ the Section 6
scoring system), **F2** Public Policy Bank, **F3** Alert Page, **F4** Admin
Settings. F5 (Coordinated-Network Detector) is out of scope for this version.

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
| **F1** Claim Repository Bank | [api/claims.md](api/claims.md) |
| **F2** Public Policy Bank | [api/policies.md](api/policies.md) |
| **F3** Alert Page | [api/alerts.md](api/alerts.md) |
| **F4** Admin Settings + utilities | [api/settings.md](api/settings.md) |
| AI service callbacks | [api/internal.md](api/internal.md) |

## Other files

- [sql/00_ai_reference_schema.sql](sql/00_ai_reference_schema.sql) — the AI
  team's DDL, for bootstrapping a **local** database only. Never executed by the
  app; never run it against shared Supabase.

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
| F1 | Claim Repository Bank | `GET /claims/repository`, `GET /claims`, `GET /claims/:id`, `PUT /claims/:id/status` |
| F2 | Public Policy Bank | `GET /policies`, `POST /policies`, `GET /policies/:id`, `GET /policies/:id/file` |
| F3 | Alert Page | `GET /alerts`, `POST /alerts`, `PATCH /alerts/:claimId/chart`, `GET /alerts/chart` |
| F4 | Admin Settings | `GET|PUT /settings/alert-threshold`, `POST /admin/generate-generic-claim` |
