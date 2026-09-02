# F5 — Coordinated-Network Detector

All routes require a Bearer token. There are **no roles** in this build, so any
authenticated user can review networks, manage the allowlist, change detector
parameters and read the export audit log — see [auth.md](auth.md) and
`docs/local_docs/PRD-v1.4.md` 3.3 for why that is a deliberate decision rather
than an omission. The safety property these endpoints rely on is
**attribution**: every status change, allowlist edit, parameter change and
export records who did it and why.

---

## Before you start: two things about the detector

The detection *maths* runs in the AI service. This backend reads its output,
governs it, and presents it.

**1. The tables have to exist.** The pipeline is built and deployed on the AI
side, but until its schema has been created against the database this backend is
pointed at, those tables are absent and every endpoint on this page answers:

```json
{
  "success": false,
  "message": "the coordinated-network detector is not available yet",
  "error": { "code": "SERVICE_UNAVAILABLE" }
}
```

F1–F4 are unaffected — and so is the US61 claim badge, which simply does not
appear. See "Cross-links into F1" at the bottom.

**2. `high` confidence is currently unreachable.** Three of the five signal
families have no data to run on in today's ingestion schema — co-amplification
has no reshare/reply fields, provenance has only handle and creation timing, and
structural overlap has no follower graph. Two or more unavailable families cap a
run at Medium regardless of score, which is the pipeline behaving correctly, not
a bug in the band logic. Every response carries `signals_unavailable` so the
reason is visible rather than inferred, and Synchrony — the one family that does
compute — currently runs on ingest time rather than publish time. See
[AI-INTEGRATION.md](../AI-INTEGRATION.md#where-flow-7-actually-stands) for the
per-family breakdown and what would lift the cap.

Three smaller consequences worth knowing before wiring a UI against them:
`shared_span_start`/`shared_span_end` on evidence posts are always `null`, so
there are no highlight offsets to render inside a near-duplicate pair;
`relabelled` is never set by either side; and comparison-role accounts carry no
`layout_x`/`layout_y`, so the graph view has to position them itself or leave
them out.

---

## Vocabulary

| Term | Meaning |
|---|---|
| **Coordination Score** | 0–100 composite over the five signals. Never displayed without `why_flagged`. |
| **SY / DU / CO / PR / AU** | Synchrony, Duplication, Cohesion, Provenance anomaly, Automation & behavioural anomaly. Each 0–100. |
| **SignalBreadth** | How many signal families independently scored ≥ 50. Computed by the pipeline, never recomputed here. |
| **Confidence band** | `low` / `medium` / `high`. Computed. A human never sets it. |
| **Review status** | `unreviewed` / `under_review` / `confirmed` / `dismissed_false_positive` / `action_taken`. A human sets it. Deliberately **not** the F1 claim status set. |

Band and review status are **orthogonal axes**. No gate in this API is
expressed as a disjunction across the two.

---

## GET /api/v1/networks

The F5 main page (US43–US48).

| Param | Values | Notes |
|---|---|---|
| `status` | a review status | Omit for all. |
| `confidence` | `medium,high` | Comma-separated bands. |
| `show_low_confidence` | `true` / `false` (default) | US43's toggle. Low networks are returned de-emphasised (`low_confidence: true`), never silently mixed in. |
| `claim_ids`, `topic_ids`, `policy_ids` | comma-separated UUIDs | |
| `q` | free text | Label and member handles. |
| `detected_from`, `detected_to` | RFC3339 or `YYYY-MM-DD` | |
| `sort` | `score` (default), `detected_at`, `accounts`, `posts`, `recurrences` | US48. |
| `page`, `limit` | | See [pagination](README.md#pagination). |

```bash
curl "http://localhost:8080/api/v1/networks?confidence=medium,high&sort=score" \
  -H "Authorization: Bearer $TOKEN"
```

**200 OK**

```json
{
  "success": true,
  "message": "coordinated networks",
  "data": {
    "networks": [
      {
        "id": "8f1c...",
        "label": "Flood-gate amplification cluster",
        "coordination_score": 78.4,
        "confidence_band": "high",
        "signal_breadth": 3,
        "review_status": "unreviewed",
        "account_count": 47,
        "post_count": 612,
        "platforms": ["x", "facebook"],
        "detected_at": "2026-08-29T04:12:00Z",
        "primary_claim": { "id": "1a2b...", "statement": "The new flood gates...", "overlap_ratio": 0.71 },
        "recurrence": { "is_recurrence": false, "occurrence_count": 1 },
        "low_confidence": false,
        "from_truncated_run": false
      }
    ],
    "status_counts": { "unreviewed": 4, "confirmed": 1, "dismissed_false_positive": 2 },
    "low_confidence_shown": false,
    "applied_sort": "score"
  },
  "meta": { "page": 1, "limit": 20, "total": 7, "total_pages": 1 }
}
```

`from_truncated_run` is on the **card**, not only the detail page: a truncated
run has known incomplete recall, triage happens on the list, and the caveat
changes what the score means.

---

## GET /api/v1/networks/:id

The detail page (US49, US50). Returns everything the card carries, plus:

| Field | What it is |
|---|---|
| `run` | The detection run: window, parameters in force, truncation, signal families unavailable. |
| `why_flagged` | **The US50 panel.** Per-signal score, plain-language method, raw counts, weight, availability; the confidence explanation naming the rule that produced the band; cluster structure; the claim-relevance block; and the stated known limitations. |
| `linked_claims`, `linked_policies` | Policies are resolved transitively through the linked claims, in the identical shape F1 returns. |
| `review` | Current human assessment, absent when unreviewed. |
| `disclaimer` | PRD 10.9.2's standing text, served rather than hard-coded so the page and the PDF cannot drift. |
| `export` | Whether a report may be generated, and if not, which condition fails. |

The composite is never returned without `why_flagged`. That is the F5
counterpart of US23's rule for claim scores, and it carries the same weight:
a number a policy reviewer cannot interrogate is not evidence.

`why_flagged.signals[].available` distinguishes "this family could not be
measured this run" from a score of zero, which is a measurement.

---

## PUT /api/v1/networks/:id/status

Record a human assessment (US52).

```json
{
  "status": "dismissed_false_positive",
  "reason": "Members are the flood-response volunteer network; the synchrony is their shift rota."
}
```

`reason` is **required, minimum 20 characters** — unlike F1's optional claim
review notes. A network assessment without a stated reason is not recordable:
it is the input both the allowlist and the §10.9.3 recalibration analysis learn
from.

Every change appends to `cis_network_review_log` together with a **write-time
snapshot of the network's signal profile**. Reading those scores back later
would not work: a re-run recomputes them, and an aggregate built on drifting
profiles cannot answer which signal is systematically over-triggering.

**200 OK** — `{ network_id, from_status, status, reason, reviewed_at, reviewed_by }`

## GET /api/v1/networks/:id/review-log

The append-only history, newest first. `limit` defaults to 100. Each entry
carries `signal_profile` as it stood at the moment of that decision.

---

## The evidence surfaces

| Endpoint | Story | Returns |
|---|---|---|
| `GET /networks/:id/graph` | US51 | Nodes with **precomputed** ForceAtlas2 coordinates, edges with per-signal weights, and the comparison set — genuine unclustered accounts active on the same claim, marked `role: "comparison"`. Layout is not recomputed client-side: PRD 10.8 requires the PDF and the screen to render identically. |
| `GET /networks/:id/timeline` | US53 | Burst bins with z-scores and an anomaly flag. |
| `GET /networks/:id/content` | US54 | Representative posts grouped by duplicate group, with the shared span offsets highlighted. Rendered from the snapshot and **never re-fetched**, which is why a deleted post still appears, marked no longer publicly available. |
| `GET /networks/:id/accounts` | US55 | The account annex. `role` = `member` / `comparison`, `q`, `sort` = `handle`, `posts_in_cluster`, `duplication_rate`, `centrality`, `created_at_platform`, `circadian_coverage`, `median_interpost`. |
| `GET /networks/:id/accounts/:accountId` | US55 | The drawer: that account's posts **and the specific edges, with their per-signal weights, that connected it**. No account may appear in a network without a viewable reason. |
| `GET /networks/:id/accounts.csv` | US57 | CSV download. The export is written to the audit log **before** the bytes are sent. |

---

## Reports and evidence bundles

### POST /api/v1/networks/:id/reports

Generates the 10-section PDF (US58, US59). Body is optional; it selects the
report type, the sections, and the redaction settings.

**The export gate is an allowlist, and it is fail-closed.** A report may be
generated only from a network whose review status is `under_review`,
`confirmed`, or `action_taken`. An `unreviewed` network and a dismissed one are
both refused, with `422` naming the failing condition — the same condition
`GET /networks/:id` reports up front in `export.reason`, so the UI can disable
the action for the server's reason rather than guessing at the rule.

Ordering inside the request is deliberate: the gate is evaluated first, then the
**audit row is written before the document is rendered**, because PRD 10.8
item 10 prints the audit entry id inside the document. "Log the export after it
succeeds" would produce a report with an empty chain-of-custody slot. The file
is uploaded before the report row is written, so a row never offers a download
that 404s.

**201 Created** — the report view, including `file_sha256`, `version`, and the
download path.

### POST /api/v1/networks/:id/evidence-bundle

The US60 ZIP: the PDF, the network JSON, the accounts and posts CSVs, and a
manifest whose hashes establish that the bundle was not modified after
generation. Same gate, same ordering.

### GET /api/v1/networks/:id/reports

Every artefact generated for this network, newest first. Reports are versioned
and **never overwritten** — one already sent to a platform stays
re-downloadable exactly as it was sent.

### GET /api/v1/reports/:reportId/file

Streams the file. Addressed by report id rather than nested under a network,
because a report outlives the page it was generated from: an audit entry links
to it directly, and so does a colleague's bookmark.

The response carries `X-Content-SHA256`, so a recipient can verify the download
against what was recorded without a second request.

---

## Detection runs

| Endpoint | Purpose |
|---|---|
| `GET /api/v1/detection-runs` | Run history. Filters: `status`, `trigger` (`scheduled`/`velocity`/`on_demand`), `truncated`, `from`, `to`. |
| `GET /api/v1/detection-runs/:id` | One run. |
| `POST /api/v1/admin/detection-runs` | Trigger on demand: `{"claim_ids": ["..."]}`. |

The read side is deliberately **not** under `/admin`: truncation and unavailable
signal families explain why a network is banded where it is, which is an
analyst's question, not an operator's. "Why is everything Medium this week?" is
a question about runs.

A run over a Non-Existing/Synthetic claim is rejected with `422`. PRD 10.3 puts
S2 out of scope for detection: a predicted claim has no real posts, so there is
nothing to cluster.

Runs also start on their own — see the detection tick and the velocity trigger
in [ARCHITECTURE.md](../ARCHITECTURE.md#background-jobs).

---

## The allowlist

Accounts the team has **declared** as legitimately coordinating: NGOs,
newsrooms, campaign groups, unions, government, and the city's own comms estate
(`self_exclusion`). This is the one place the read direction between the two
services reverses — the backend owns the list and the pipeline consumes it.

US63 asks for it to be seeded during onboarding with the city's known
civil-society partners, *before the first detection run, not after the first
false positive*. Building it late means the first thing the tool does in
production is accuse an NGO.

| Endpoint | Purpose |
|---|---|
| `GET /api/v1/admin/allowlist` | List. Filters: `q`, `platform`, `category`, `include_removed`. |
| `GET /api/v1/admin/allowlist/categories` | Counts per category. |
| `POST /api/v1/admin/allowlist` | Add entries. |
| `PATCH /api/v1/admin/allowlist/:id` | Change category or reason. |
| `DELETE /api/v1/admin/allowlist/:id` | Remove. **A removal reason is required** and is stored separately from the addition reason — overwriting the latter would destroy the record of why the entry existed. |
| `POST /api/v1/networks/:id/allowlist` | Allowlist a whole network's membership (US56). |
| `POST /api/v1/networks/:id/accounts/:accountId/allowlist` | Allowlist one member (US56). |
| `GET|POST /api/v1/admin/common-phrases`, `DELETE .../:id` | The phrase allowlist: slogans and civic boilerplate excluded from duplication scoring, so a shared campaign hashtag is not read as content duplication. |

Entries are keyed on `(platform, platform_account_id)`, not on the handle.
Handles get renamed; the platform-issued id does not, so protection keyed on the
handle alone would lapse the moment an NGO rebranded.

Allowlisting is retroactive: it suppresses and relabels the account's historical
networks, and the response summarises exactly which networks were affected and
which of them have already been exported — a PDF citing an account since
allowlisted is already in someone's inbox.

A network whose membership is ≥ 60% allowlisted is **suppressed entirely, on
every surface**, including the F1 claim badge (PRD 10.6.3 rule 5): a network
invisible in F5 must not be reachable through F1.

---

## Governance and recalibration

| Endpoint | Story | Purpose |
|---|---|---|
| `GET /api/v1/admin/offtopic-clusters` | US62 | Genuinely coordinated clusters that failed the claim-relevance gate — spam rings, engagement farms, unrelated political amplification. They are **not** the city's problem and must never appear in a climate report. Retained only so an admin can see whether `omega_min` is too loose or too tight. Filters: `run_id`, `claim_id`, `failed_test` (`anchoring` / `evidence_volume` / `link_strength`), `from`, `to`. |
| `GET /api/v1/admin/offtopic-clusters/rates` | US62 | The off-topic rate per run — the trend that actually answers the threshold question. |
| `GET /api/v1/admin/dismissals` | 10.9.3 | Every false-positive dismissal with its reason **and its signal profile**. |
| `GET /api/v1/admin/dismissals/summary` | 10.9.3 | The aggregate: dismissal rate, precision against the PRD's target, and which signals over-trigger. `window_days` defaults to 90. |
| `GET /api/v1/admin/export-audit` | US64 | Who exported what, when, with which sections and redaction settings. Filters: `user_id`, `network_id`, `run_id`, `export_type`, `from`, `to`. |

Whether dismissals should auto-adjust the signal weights `β_k` is an **open
question in the PRD**, and the current answer is no: these endpoints report, an
admin decides. Automatic adjustment risks silent drift in a system whose
defensibility rests on stated parameters.

---

## Detector settings

Documented with the rest of F4 in [settings.md](settings.md) — `GET|PUT
/api/v1/settings/detector`, plus `/detector/ranges` and `/detector/history`.

---

## Cross-links into F1

`GET /claims/:id`, `GET /claims`, `GET /claims/repository` and
`GET /policies/:id` carry an optional `coordinated_network` object on the claim
detail and on every claim card (US61). It appears only when **all four** of
these hold, and the gate is built as an explicit fail-closed allowlist:

1. a `network_claim_link` row exists for the claim with `passed_relevance_gate = true`;
2. the network's confidence band is `medium` or `high`;
3. its review status is **not** `dismissed_false_positive`;
4. it is not suppressed under PRD 10.6.3.

When nothing qualifies the field is **omitted, not null** — the PRD is explicit
that there is no empty state.

Condition 3 is not decoration. Without it a claim page badges a network the team
has already examined and concluded was organic: a government telling its own
analysts that residents it cleared are a coordinated network.

See [claims.md](claims.md) for the field shape.
