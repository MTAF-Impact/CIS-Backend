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
`buildBreakdown` (`internal/service/claim_service.go`).

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
ClaimScore = 0.15·R + 0.15·V + 0.30·F + 0.30·H + 0.10·EI
```

Weights sum to exactly 1.00 and every input is bounded to [0, 100], so
`claim_score` is guaranteed to land in [0, 100].

**Weight rationale:** Falseness and Harm carry 0.60 combined because CIS is a
risk-triage tool, not a virality tracker. Reach and Velocity contribute 0.30 for
urgency of spread. Emotional Intensity is weighted lowest at 0.10.

These are returned as `weights` in every `score_breakdown` so the UI can explain
the ranking without hardcoding constants.

---

## Net Pushback Ratio (PRD 6.4)

Captures how much the public is already self-correcting a claim, and discounts
the score accordingly — **without ever erasing it**.

```
NPR            = OpposingVolume / (SupportingVolume + OpposingVolume)     ∈ [0,1]
DiscountFactor = 1 − (γ × NPR)              with γ = 0.5  ⇒ ∈ [0.5, 1]
FinalClaimScore = ClaimScore × DiscountFactor                            ∈ [0,100]
```

γ caps the dampening: even total pushback (NPR = 1) reduces a score by at most
50%. Measured over a rolling 24–48 hour window.

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

A `null` value means the AI service has not computed it yet. It is not zero, and
the UI should not render it as such.

---

## Where the score is used

| Surface | Use |
|---|---|
| S1 cards (US10) | `final_claim_score` badge |
| S1 ranking (US7) | Top 10 by `final_claim_score DESC` — `NULLS LAST`, so an unscored claim never outranks a scored one |
| Detail page (US23) | Full breakdown |
| F3 watchlist (US29) | Compared against the F4 global threshold for Over/Under Threshold |
| F3 chart (US27) | Plotted over time on a fixed 0–100 axis |

The chart's history comes from the backend's own `cis_claim_score_snapshots`
table, because the AI service stores only the current value. See
[api/alerts.md](api/alerts.md).
