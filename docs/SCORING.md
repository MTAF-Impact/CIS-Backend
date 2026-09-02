# Claim Scoring System — As Implemented

Reference for PRD Section 6 as it is exposed by this API.

## Division of labour

> **The AI service computes every value. This backend reads and presents them.**

The backend never recalculates or writes a score. What it owns is the
*presentation contract*:

- publishing the weights so the ranking is explainable (PRD 6.3, 6.2.4)
- clamping values to their documented ranges — PRD 6.3 and 6.4.4 both say
  implementations should "still assert the bound"
- enforcing the US25 dormancy rules about when NPR must be shown as
  not-applicable
- returning every component **together with** the collapsed score (US23)

Implementation: `internal/scoring/scoring.go`, applied in
`ClaimService.buildBreakdown` (`internal/service/claim_service.go`).

### The weights are configuration, not constants

Every weight and threshold below is admin-editable in F4 and stored in
`cis_settings`; the numbers shown here are the seeded defaults. The backend
reads them live — including the plain-language formula tooltip, which is
generated from the current values rather than written out, so the words and the
numbers cannot drift apart after a retune.

**Both services read the same rows**, which is the point: a weight the API
published but the AI service did not score under would make the breakdown a
description of a ranking the system does not perform. See
`docs/local_docs/FE_DYNAMIC_PARAMETER.md` and
`docs/local_docs/AI_DYNAMIC_PARAMETER.md`.

**Only Existing/Generic claims are scored** (US4). Synthetic claims have no
score, and the API omits the fields rather than sending zeros.

---

## The five parameters (PRD 6.2)

All normalized to **0–100** before weighting.

| | Parameter | Column | Meaning |
|---|---|---|---|
| **R** | Reach & Spread | `reach_score` | How far the claim has travelled. Log-scaled over impressions, unique authors, content count, and platform spread, then min-max normalized over a trailing window. |
| **V** | Velocity | `velocity_score` | How fast it is growing right now, as a z-score against the topic's own baseline — so a spike means the same thing in a noisy topic as in a quiet one. |
| **F** | Falseness Confidence | `falseness_score` | Semantic similarity to verified official sources × 100. Below the confidence threshold, a claim gets no F score and is routed to the unverified queue. |
| **H** | Harm Severity | `harm_score` | Estimated real-world damage. Weighted sum of four sub-scores. |
| **EI** | Emotional/Moral Intensity | `emotional_intensity_score` | How provoked the reaction is: outrage-word density and negative-reaction ratio. |

### Scope (PRD 6.1.1)

R, V, F, H, and EI are computed **exclusively on Supporting-side content** —
they answer one question: how dangerous is the claim itself, right now.
Opposing-side content enters the pipeline in exactly one place, the NPR
(§6.4).

This is why the Top 5 Accounts panel (US12) also ranks over Supporting-side
content only: it shares the Reach parameter's scope.

### Harm sub-scores (PRD 6.2.4)

```
H = 0.35·PublicSafety + 0.30·InstitutionalTrust + 0.20·Economic + 0.15·PolicyDisruption
```

Weights sum to 1.00, so H is natively bounded to [0, 100]. Returned in
`harm_breakdown` alongside `weights` and `human_confirmed`.

**Policy Disruption is weighted lowest (0.15) on purpose:** scoring "criticism of
a government's own policy" as harm carries inherent bias risk. PRD §5.6 asks
that the UI not style it in a way that overstates its influence.

---

## Composite Claim Score (PRD 6.3)

```
ClaimScore = 0.15·R + 0.15·V + 0.30·F + 0.30·H + 0.10·EI      (defaults)
```

Weights sum to exactly 1.00 and every input is bounded to [0, 100], so
`claim_score` is guaranteed to land in [0, 100]. The sum is enforced on save
(`scoring.weight_*`, keys AP-01…AP-05): a set that does not total 1.00 is
rejected, because one that did would silently deflate every score in the bank.

**Weight rationale:** Falseness and Harm carry 0.60 combined because CIS is a
risk-triage tool, not a virality tracker. Reach and Velocity contribute 0.30 for
urgency of spread. Emotional Intensity is weighted lowest at 0.10.

These are returned as `weights` in every `score_breakdown` so the UI can explain
the ranking without hardcoding constants.

**When F is `null`, the AI service drops its weight and renormalises the other
four over `1 − weight_falseness`** rather than substituting `0`, which would assert "confirmed
true" and depress every claim in the bank. The `weights` this backend publishes
are the nominal five above, so on a claim with no F they describe the formula
rather than that claim's own arithmetic. Given the `official_sources` corpus is
currently empty — see **Score Transparency Requirement** below — that is the
common case, not the exception.

---

## Net Pushback Ratio (PRD 6.4)

Captures how much the public is already self-correcting a claim, and discounts
the score accordingly — **without ever erasing it**.

```
NPR            = OpposingVolume / (SupportingVolume + OpposingVolume)     ∈ [0,1]
DiscountFactor = 1 − (γ × NPR)              with γ = 0.5  ⇒ ∈ [0.5, 1]
FinalClaimScore = ClaimScore × DiscountFactor                            ∈ [0,100]
```

γ (`scoring.discount_gamma`, AP-15) caps the dampening: at the default 0.5, even
total pushback (NPR = 1) reduces a score by at most 50%. `DiscountFactor` is
clamped to `[1 − γ, 1]` rather than to a hardcoded `[0.5, 1]`, so lowering γ
narrows the floor with it instead of admitting values the configuration says are
impossible.

The rolling window is `scoring.npr_window_hours` (AP-14, default 36 h, within
PRD 6.4.3's recommended 24–48 h band).

Stance classification: `supporting` spreads the claim, `opposing` disputes or
corrects it, `neutral` mentions it without taking a position and is **excluded**
from NPR.

### `EI_opposing` is diagnostic only (PRD 6.4.6, US24)

`emotional_intensity_opposing` uses the same formula on Opposing-side posts and
is displayed beside `emotional_intensity` so a reviewer can see whether public
correction cooled the conversation or whether both sides remain charged.

**It never enters `claim_score`, `npr`, `discount_factor`, or
`final_claim_score`.** Reaction data alone cannot disambiguate its target —
agreeing with a debunker versus defending the claim — so folding it in would
break auditability.

### Edge cases (PRD 6.4.7, US25)

| Condition | Behaviour |
|---|---|
| Supporting + Opposing volume = 0 | Claim is flagged **dormant**, not discounted. |
| Total volume below the reliability threshold (~20–30 posts) | `DiscountFactor` defaults to 1 — no discount. |

The API enforces the first rule at the presentation layer: when `is_dormant` is
`true`, `npr` and `discount_factor` are returned as `null` with an explanatory
`note`, so a UI can never render a discount that was never applied.

```json
{
  "npr": null,
  "discount_factor": null,
  "final_claim_score": 84.9,
  "is_dormant": true,
  "note": "No supporting or opposing volume in the rolling window, so this claim is flagged dormant rather than discounted. NPR and DiscountFactor are not applicable (PRD 6.4.7)."
}
```

The point of both rules: a claim's priority must never be lowered on the basis
of statistically unreliable data.

---

## Score Transparency Requirement (PRD 6.5, US23)

| Value | Range | Purpose |
|---|---|---|
| R, V, F, H, EI | 0–100 each | Which dimension is driving priority |
| `claim_score` | 0–100 | Raw risk before pushback |
| `npr` | 0–1 | Balance of supporting vs. opposing volume |
| `discount_factor` | 0.5–1 | How much pushback is reducing the score |
| `final_claim_score` | 0–100 | **The value S1 ranks by** |
| `emotional_intensity_opposing` | 0–100 | Charge on the pushback side (diagnostic) |

`GET /api/v1/claims/:id` returns all of these in a single `score_breakdown`
object. **The final number is never serialized without its inputs** — that is
the requirement, and it is why the breakdown is not a separate endpoint.

A `null` value means the AI service has not computed it. It is not zero, and the
UI should not render it as such.

**Expect `F` to be `null` on essentially every claim, indefinitely.** Falseness
is a similarity match against the AI service's `official_sources` corpus, and
that corpus is empty — no fact-check or official-statement documents have been
loaded into this deployment. The AI service leaves `F` `null` rather than `0`
precisely because `0` would assert "confirmed true", and drops F's 0.30 weight
from `claim_score`, renormalising the remaining four so the composite is not
systematically depressed. So a claim with `falseness_score: null` still has a
correct `claim_score`; it is scored on R, V, H and EI alone. This is the
designed behaviour, not a pipeline that has fallen behind.

Two additions in v1.5 sit on the same object:

- **`formula`** — the plain-language sentence behind the US23 info-tooltip,
  generated from the same configured weights the score is computed under, so the
  explanation and the arithmetic cannot drift apart — including after an admin
  retunes them in F4.
- **`harm_breakdown.edit`** — the audit trail of a human override: who, when,
  and the four sub-scores plus the composite `harm` as they were before. Present
  only once an override has happened, which is what lets the UI mark an edited H
  distinctly from an AI-original one. `human_confirmed` cannot do that job, since
  an empty confirmation also sets it.

Only the four Harm sub-components are editable (US23). R, V, F and EI remain
AI-only, and this backend still computes none of them: the edit is proxied to
the AI service, which recomputes H → ClaimScore → FinalClaimScore.

---

## Indonesia Climate Sentiment Index (PRD 6.6, new in v1.5)

The one score this backend **does** compute. It is not a claim-level value: it
asks whether the *overall* climate conversation — neutral and genuinely positive
discourse included — is trending toward trust or distrust, which is an aggregate
the AI service does not roll up.

```
CSI            = BCS_normalized × 0.5 + (100 − RiskLoad) × 0.5
BCS            = (positive − negative) / total          → −1 … +1
BCS_normalized = (BCS + 1) / 2 × 100                    → 0 … 100
RiskLoad       = Σ(FinalClaimScore_i × Volume_i) / total, for claims ≥ RiskThreshold
```

The formulas live in `internal/scoring/csi.go`, next to the claim weights they
mirror, and take their parameters as a `CSIParams` rather than reading package
constants: PRD 6.5's transparency requirement applies here too, so the one place
the arithmetic is written must be the one place it is applied.

| Parameter | Setting key | Default | Source |
|---|---|---|---|
| Component weights | `csi.weight_bcs`, `csi.weight_risk_load` | 0.5 / 0.5 | PRD 6.6 (AP-18, AP-19) |
| `RiskThreshold` | *derived from* `alert_threshold` | 70 | PRD 6.6.2 (AP-20) |
| Rolling window | `csi.window_days` | 7 days | PRD 6.6.3 |
| Momentum lag | `csi.momentum_lag_hours` | 24 h | PRD 6.6.3 |
| Minimum volume | `csi.minimum_volume` | 100 items | PRD 6.6.3 ("a defined minimum") |
| Gauge bands | `csi.band_risky_ceiling`, `csi.band_watch_ceiling` | 33.33 / 66.67 | Not specified by the PRD; documented, not hidden |

**`RiskThreshold` has no key of its own and is no longer 50.** AP-20 makes it a
derived value that always equals the global alert threshold, so "elevated risk"
means the same thing on the Alert page and on this gauge. It is served read-only
beside `alert_threshold` in the F4 catalog, and it moves the moment that does.

`Volume_i` counts a claim's Supporting **and** Opposing content only, per
6.6.2's definition; neutral content stays in the BCS denominator, where the
definition is explicitly "all climate-related content". RiskLoad is clamped to
0–100 — unlike the claim parameters it is not mathematically bounded above,
since `Volume_i` is a subset of the denominator.

Below the minimum volume the index reports `insufficient_data` rather than a
score, per 6.6.3: a quiet week must not read as a calm one. See
[api/overview.md](api/overview.md).

---

## Where the score is used

| Surface | Use |
|---|---|
| S1 cards (US10) | `final_claim_score` badge |
| S1 ranking (US7) | Top 10 by `final_claim_score DESC` — `NULLS LAST`, so an unscored claim never outranks a scored one |
| Detail page (US23) | Full breakdown |
| F3 watchlist (US29) | Compared against the F4 global threshold for Over/Under Threshold |
| F3 chart (US27) | Plotted over time on a fixed 0–100 axis, at Day/Week/Month/Year granularity |
| F3 notifications (US71) | A flip across the threshold between two evaluations raises the sidebar badge |
| F6 O1 (US67) | Above/below-threshold ratio, and the RiskLoad half of the Climate Sentiment Index |
| F6 O2/O3 (US69, US70) | Above-threshold counts and average score, combined into the treemap and leaderboard metric |

The chart's history comes from the backend's own `cis_claim_score_snapshots`
table, because the AI service stores only the current value. See
[api/alerts.md](api/alerts.md).
