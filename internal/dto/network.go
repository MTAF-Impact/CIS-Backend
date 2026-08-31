package dto

import "time"

// F5 — Coordinated-Network Detector payloads.
//
// One rule shapes every struct in this file, and it is PRD 10.9.1's third hard
// rule: the system never labels an individual account automated, inauthentic,
// or malicious. Its only claim is that a SET of accounts exhibited measurable
// coordinated behaviour within a window. So there is no `is_bot`, no
// `suspicion`, no `verdict` field anywhere below — the nouns are behaviours and
// counts, and the one judgement in the whole payload is `review_status`, which
// a person set.

// NetworkClaimRef is a claim a network is linked to, with the relevance-gate
// figures for that link.
type NetworkClaimRef struct {
	ClaimID        string    `json:"claim_id"`
	ClaimStatement string    `json:"claim_statement"`
	ClaimType      string    `json:"claim_type"`
	Topic          *TopicRef `json:"topic,omitempty"`
	IsPrimary      bool      `json:"is_primary"`

	// The claim-relevance gate's three figures (PRD 10.5.1a). US50 requires
	// them on the detail page and PRD 10.8 item 3 in the report, because they
	// answer a question the signal scores cannot: not "is this coordinated?"
	// but "is this coordinated *about our claim*?".
	OverlapRatio        float64 `json:"overlap_ratio"`
	AnchoringShare      float64 `json:"anchoring_share"`
	ClaimClusterPosts   int     `json:"claim_cluster_post_count"`
	PassedRelevanceGate bool    `json:"passed_relevance_gate"`
}

// RecurrenceInfo summarises how often a network has resurfaced (US46, US49).
type RecurrenceInfo struct {
	// Count includes the current detection, so a first sighting reads 1.
	Count       int        `json:"count"`
	FirstSeenAt *time.Time `json:"first_seen_at,omitempty"`
	// IsRecurrence is true when this detection inherited an earlier network's
	// history through parent_network_id.
	IsRecurrence bool `json:"is_recurrence"`
	// PriorClaims are the claims earlier detections in this chain were anchored
	// to. PRD 10.5.1 requires both the current primary claim and the prior
	// anchoring claims to be stated: a recurrence inherits history but NOT
	// relevance, and "this same set of accounts previously amplified claims X
	// and Y" is the sentence that makes a referral actionable.
	PriorClaims []PriorAnchorRef `json:"prior_claims,omitempty"`
}

// PriorAnchorRef is one earlier detection in a recurrence chain.
type PriorAnchorRef struct {
	NetworkID         string    `json:"network_id"`
	Label             string    `json:"label"`
	DetectedAt        time.Time `json:"detected_at"`
	ConfidenceBand    string    `json:"confidence_band"`
	CoordinationScore float64   `json:"coordination_score"`
	ClaimID           *string   `json:"claim_id,omitempty"`
	ClaimStatement    *string   `json:"claim_statement,omitempty"`
}

// RunContext is the detection-run level information a network inherits.
//
// It travels with every network rather than being fetched separately because
// two of its fields change how the network itself must be read: a truncated
// candidate set means known-incomplete recall, and two or more unavailable
// signal families caps the whole run at Medium regardless of score
// (PRD 10.6.3 rule 4). An analyst judging a network without them is judging it
// on partial information.
type RunContext struct {
	RunID              string     `json:"run_id"`
	TriggerSource      string     `json:"trigger_source"`
	WindowStart        time.Time  `json:"window_start"`
	WindowEnd          time.Time  `json:"window_end"`
	CompletedAt        *time.Time `json:"completed_at,omitempty"`
	Truncated          bool       `json:"truncated"`
	CandidatesCount    int        `json:"candidates_count"`
	SignalsUnavailable []string   `json:"signals_unavailable"`
	// ConfidenceCappedAtMedium states the consequence rather than leaving the
	// client to re-derive rule 4 from the two fields above.
	ConfidenceCappedAtMedium bool `json:"confidence_capped_at_medium"`
	// TruncationNote is rendered verbatim beside the header when Truncated is
	// set, so the caveat cannot be dropped by a client that forgot to check the
	// boolean.
	TruncationNote string `json:"truncation_note,omitempty"`
}

// NetworkCard is the F5 list representation of a network (US46).
type NetworkCard struct {
	ID    string `json:"id"`
	Label string `json:"label"`

	CoordinationScore float64 `json:"coordination_score"`
	ConfidenceBand    string  `json:"confidence_band"`
	SignalBreadth     int     `json:"signal_breadth"`
	ReviewStatus      string  `json:"review_status"`

	AccountCount int      `json:"account_count"`
	PostCount    int      `json:"post_count"`
	Platforms    []string `json:"platforms"`

	DetectedAt time.Time `json:"detected_at"`

	// PrimaryClaim is truncated on the card and complete on the detail page.
	PrimaryClaim *NetworkClaimRef `json:"primary_claim,omitempty"`

	Recurrence RecurrenceInfo `json:"recurrence"`

	// LowConfidence flags a card revealed only by the US43 toggle, so the
	// frontend can de-emphasise it without re-deriving the rule.
	LowConfidence bool `json:"low_confidence"`
	// FromTruncatedRun surfaces the run-level caveat on the card, because
	// triage happens on the list and the caveat changes what the score means.
	FromTruncatedRun bool `json:"from_truncated_run"`
}

// SignalDetail is one cluster metric with everything US50 requires beside it.
type SignalDetail struct {
	Code  string  `json:"code"` // SY | DU | CO | PR | AU
	Name  string  `json:"name"`
	Score float64 `json:"score"` // 0-100
	// Method is the one-sentence plain-language description US50 requires: "a
	// policy reviewer must be able to read this panel without knowing what
	// conductance is."
	Method string `json:"method"`
	// RawCounts is the underlying observation behind the normalised score, not
	// just the score. US50's example: "43 of 47 accounts posted within the same
	// 6-minute window, 3 times in 24h".
	RawCounts any `json:"raw_counts,omitempty"`
	// Weight is this metric's share of the composite (PRD 10.5.5).
	Weight float64 `json:"weight"`
	// Available is false when the family behind this metric could not be
	// measured this run. Distinguished from a score of zero, which is a
	// measurement.
	Available bool `json:"available"`
}

// ConfidenceExplanation states which banding rule produced the band and why
// (US50).
type ConfidenceExplanation struct {
	Band          string `json:"band"`
	SignalBreadth int    `json:"signal_breadth"`
	// Rule is the condition that was applied, written out.
	Rule string `json:"rule"`
	// CappedByRun records that PRD 10.6.3 rule 4 held this network below the
	// band its score alone would have earned.
	CappedByRun bool `json:"capped_by_run"`
	// Note carries the guard's rationale where it is load-bearing — a high
	// composite with SignalBreadth = 1 can never reach High, by design.
	Note string `json:"note,omitempty"`
}

// WhyFlagged is the US50 "Why this was flagged" panel: the F5 counterpart of
// US23's score breakdown, carrying the same hard constraint that the composite
// must never be displayed without access to this.
type WhyFlagged struct {
	CoordinationScore  float64               `json:"coordination_score"`
	Signals            []SignalDetail        `json:"signals"`
	Confidence         ConfidenceExplanation `json:"confidence"`
	SignalsUnavailable []string              `json:"signals_unavailable"`

	// Structure carries the two cluster-shape figures PRD 10.8 item 5 requires
	// in the report alongside the graph.
	InternalDensity        float64 `json:"internal_density"`
	Conductance            float64 `json:"conductance"`
	ComparisonAccountCount int     `json:"comparison_account_count"`

	// ClaimRelevance is the block US50 requires: overlap_ratio against the
	// primary claim, the member anchoring share, the cluster's claim-cluster
	// post count, and any secondary claim links.
	ClaimRelevance ClaimRelevanceBlock `json:"claim_relevance"`

	// KnownLimitations are the caveats PRD requires to be stated rather than
	// left implicit — 10.5.2.1's timezone confound in particular.
	KnownLimitations []string `json:"known_limitations"`
}

// ClaimRelevanceBlock answers "is this coordinated *about our claim*?" (US50).
type ClaimRelevanceBlock struct {
	PrimaryClaim    *NetworkClaimRef  `json:"primary_claim,omitempty"`
	SecondaryClaims []NetworkClaimRef `json:"secondary_claims"`
	// Thresholds are the gate values in force for this run, so the figures
	// above can be read against the bar they had to clear.
	AnchorShareThreshold     float64 `json:"anchor_share_threshold"`
	MinClaimPostsThreshold   int     `json:"min_claim_posts_threshold"`
	MinLinkStrengthThreshold float64 `json:"min_link_strength_threshold"`
}

// NetworkDetail is the US49/US50 network detail page payload.
type NetworkDetail struct {
	NetworkCard

	Run RunContext `json:"run"`

	WhyFlagged WhyFlagged `json:"why_flagged"`

	LinkedClaims   []NetworkClaimRef `json:"linked_claims"`
	LinkedPolicies []PolicyRef       `json:"linked_policies"`

	Review *NetworkReview `json:"review,omitempty"`

	// Disclaimer is PRD 10.9.2's standing text, required verbatim on every
	// report AND on the network detail page. It is served rather than
	// hard-coded in the frontend so the two renderings can never drift.
	Disclaimer string `json:"disclaimer"`

	// Export describes whether this network may be exported and, when not, why.
	// Served so the UI can disable the action for the same reason the server
	// would refuse it, rather than guessing at the rule.
	Export ExportEligibility `json:"export"`
}

// ExportEligibility is US58's gate, evaluated and explained.
type ExportEligibility struct {
	Allowed bool `json:"allowed"`
	// Reason is empty when Allowed. When not, it names the failing condition.
	Reason string `json:"reason,omitempty"`
	// AllowedStatuses is the allowlist itself, so a client can render the rule.
	AllowedStatuses []string `json:"allowed_statuses"`
}

// NetworkReview is the current human assessment of a network (US52).
type NetworkReview struct {
	Status     string     `json:"status"`
	Reason     string     `json:"reason"`
	ReviewedBy *string    `json:"reviewed_by"`
	ReviewedAt *time.Time `json:"reviewed_at"`
}

// NetworkReviewLogEntry is one recorded status change (US52).
type NetworkReviewLogEntry struct {
	ID         string    `json:"id"`
	FromStatus string    `json:"from_status"`
	ToStatus   string    `json:"to_status"`
	Reason     string    `json:"reason"`
	UserID     *string   `json:"user_id"`
	CreatedAt  time.Time `json:"created_at"`
	// SignalProfile is the network's scores as they stood at the moment of the
	// decision, copied in rather than joined — a later run can recompute them,
	// and an aggregate built on drifting profiles cannot answer which signal is
	// systematically over-triggering (PRD 10.9.3).
	SignalProfile any `json:"signal_profile,omitempty"`
}

// UpdateNetworkStatusRequest is the body of PUT /networks/:id/status (US52).
//
// The reason is REQUIRED and at least 20 characters, unlike claim review notes
// which are optional. A network assessment without a stated reason is not
// recordable: it is the input the allowlist and the recalibration analysis both
// learn from.
type UpdateNetworkStatusRequest struct {
	Status string `json:"status" validate:"required,oneof=unreviewed under_review confirmed dismissed_false_positive action_taken"`
	Reason string `json:"reason" validate:"required,min=20,max=4000"`
}

// NetworkStatusResponse confirms a status change.
type NetworkStatusResponse struct {
	NetworkID  string    `json:"network_id"`
	FromStatus string    `json:"from_status"`
	Status     string    `json:"status"`
	Reason     string    `json:"reason"`
	ReviewedAt time.Time `json:"reviewed_at"`
	ReviewedBy *string   `json:"reviewed_by"`
}

// GraphNode is one account in the US51 force-directed graph.
type GraphNode struct {
	AccountID string `json:"account_id"`
	Handle    string `json:"handle"`
	Platform  string `json:"platform"`
	// Role is "member" or "comparison". Comparison nodes are genuine
	// unclustered accounts active on the same claim, rendered in a visually
	// distinct style for contrast (US51) — they are what lets an analyst see
	// that the cluster is unusual relative to the ordinary conversation.
	Role string `json:"role"`

	// Size is driven by centrality, per US51.
	DegreeCentrality      float64 `json:"degree_centrality"`
	EigenvectorCentrality float64 `json:"eigenvector_centrality"`
	PostsInCluster        int     `json:"posts_in_cluster"`

	// X and Y come from the stored ForceAtlas2 layout so the UI and the PDF
	// render identically and reports stay byte-deterministic (PRD 10.8).
	X *float64 `json:"x,omitempty"`
	Y *float64 `json:"y,omitempty"`

	// Allowlisted marks a member since declared legitimate coordination.
	Allowlisted bool `json:"allowlisted"`
}

// GraphEdge is one retained behavioural edge with its per-signal decomposition,
// which is what US51's edge hover shows and what makes membership explainable.
type GraphEdge struct {
	Source string `json:"source"`
	Target string `json:"target"`

	Weight float64 `json:"weight"`
	// Signals is the per-family breakdown: this pair scored these values on
	// these axes. Absent from no edge — PRD 10.5.3 requires every retained edge
	// to store it.
	Signals EdgeSignals `json:"signals"`
	// SignalCount is how many families cleared the multi-signal threshold.
	SignalCount int `json:"signal_count"`
}

// EdgeSignals is one edge's five pairwise similarities.
type EdgeSignals struct {
	Time   float64 `json:"w_time"`
	Text   float64 `json:"w_text"`
	Amp    float64 `json:"w_amp"`
	Meta   float64 `json:"w_meta"`
	Struct float64 `json:"w_struct"`
}

// NetworkGraph is the US51 payload.
type NetworkGraph struct {
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`

	// Reduced records that the graph was rendered as its k-core because the
	// full node count exceeded the legibility limit. US51: "Must remain legible
	// up to ~300 nodes; beyond that, render the k-core and note the reduction."
	Reduced         bool   `json:"reduced"`
	ReductionNote   string `json:"reduction_note,omitempty"`
	TotalNodeCount  int    `json:"total_node_count"`
	MemberCount     int    `json:"member_count"`
	ComparisonCount int    `json:"comparison_count"`
}

// BurstBin is one bin of the US53 timeline.
type BurstBin struct {
	BinStart    time.Time `json:"bin_start"`
	PostCount   int       `json:"post_count"`
	ZScore      float64   `json:"zscore"`
	IsAnomalous bool      `json:"is_anomalous"`
}

// BurstTimeline is the US53 payload. The bin width is stated rather than
// implied, because a burst chart is unreadable without knowing what a bar spans.
type BurstTimeline struct {
	BinWidthSeconds int        `json:"bin_width_seconds"`
	WindowStart     time.Time  `json:"window_start"`
	WindowEnd       time.Time  `json:"window_end"`
	Bins            []BurstBin `json:"bins"`
	AnomalousCount  int        `json:"anomalous_count"`
}

// EvidencePost is one snapshotted post (US54).
type EvidencePost struct {
	ID             string    `json:"id"`
	AccountID      string    `json:"account_id"`
	Handle         string    `json:"handle"`
	Platform       string    `json:"platform"`
	PostPlatformID string    `json:"post_platform_id"`
	Text           string    `json:"text"`
	PostedAt       time.Time `json:"posted_at"`
	CapturedAt     time.Time `json:"captured_at"`
	ContentSHA256  string    `json:"content_sha256"`
	IsCanonical    bool      `json:"is_canonical"`

	// StillPublic is false for a post deleted since capture. US54 requires it
	// to remain visible, marked — the snapshot is the evidence, and content
	// disappearing is the normal end of a campaign, not a gap in the record.
	StillPublic bool `json:"still_public"`
	// Availability is the label to render, so the marker text is identical in
	// the UI and the PDF.
	Availability string `json:"availability"`

	// SharedSpanStart/End locate the span this variant shares with the group's
	// canonical text. US54 requires the shared span highlighted and PRD 10.8
	// item 6 requires the same in the report, so the offsets are computed
	// server-side rather than in the browser.
	SharedSpanStart *int `json:"shared_span_start,omitempty"`
	SharedSpanEnd   *int `json:"shared_span_end,omitempty"`
}

// DuplicateGroup is one cluster of near-identical posts (US54).
type DuplicateGroup struct {
	GroupID       string         `json:"group_id"`
	CanonicalText string         `json:"canonical_text"`
	VariantCount  int            `json:"variant_count"`
	Variants      []EvidencePost `json:"variants"`
}

// RepresentativeContent is the US54 payload.
type RepresentativeContent struct {
	Groups []DuplicateGroup `json:"groups"`
	// Ungrouped posts belong to no duplicate group. They are returned so the
	// evidence set is complete rather than only its most incriminating part.
	Ungrouped []EvidencePost `json:"ungrouped,omitempty"`
	Note      string         `json:"note"`
}

// AccountAnnexRow is one row of the US55 account annex.
//
// Every column here is a measured behaviour or a graph position. None is a
// verdict: PRD 10.9.1 rule 3 forbids the system labelling an individual account
// automated, so "circadian coverage 1.00" is reported and "no sleep cycle,
// therefore a bot" is not.
type AccountAnnexRow struct {
	AccountID         string     `json:"account_id"`
	Handle            string     `json:"handle"`
	Platform          string     `json:"platform"`
	PlatformAccountID string     `json:"platform_account_id"`
	CreatedAtPlatform *time.Time `json:"created_at_platform"`

	PostsInCluster        int      `json:"posts_in_cluster"`
	DuplicationRate       float64  `json:"duplication_rate"`
	MedianInterpostSecs   *float64 `json:"median_interpost_interval_seconds"`
	CircadianCoverage     float64  `json:"circadian_coverage"`
	DegreeCentrality      float64  `json:"degree_centrality"`
	EigenvectorCentrality float64  `json:"eigenvector_centrality"`

	// ScoreContribution is this account's individual contribution to each
	// cluster metric (PRD 10.5.6 item 4).
	ScoreContribution any `json:"score_contribution,omitempty"`

	Role        string `json:"role"`
	Allowlisted bool   `json:"allowlisted"`
}

// AccountDrawer is the US55 per-account view: the account's posts in the
// cluster and the specific edges that connected it.
//
// This exists because of one sentence in US55 — "No account may appear in a
// network without a viewable reason" — and it is the endpoint that makes the
// sentence true.
type AccountDrawer struct {
	Account AccountAnnexRow `json:"account"`
	Posts   []EvidencePost  `json:"posts"`
	// ConnectingEdges are the edges, with per-signal weights, that placed this
	// account in the network.
	ConnectingEdges []GraphEdge `json:"connecting_edges"`
	// Explanation renders the answer in words, for the same reason US50
	// requires plain-language method descriptions.
	Explanation string `json:"explanation"`
}

// --- Allowlist (US56, US63) ---

// AllowlistEntry is one protected account.
type AllowlistEntry struct {
	ID                string     `json:"id"`
	Platform          string     `json:"platform"`
	PlatformAccountID string     `json:"platform_account_id"`
	Handle            string     `json:"handle"`
	Category          string     `json:"category"`
	Reason            string     `json:"reason"`
	AddedBy           *string    `json:"added_by"`
	AddedAt           time.Time  `json:"added_at"`
	RemovedBy         *string    `json:"removed_by,omitempty"`
	RemovedAt         *time.Time `json:"removed_at,omitempty"`
	RemovalReason     *string    `json:"removal_reason,omitempty"`
	Active            bool       `json:"active"`
}

// AddAllowlistRequest marks a network or a single account as legitimate
// coordination (US56).
type AddAllowlistRequest struct {
	Category string `json:"category" validate:"required,oneof=ngo newsroom campaign_group government union other self_exclusion"`
	Reason   string `json:"reason" validate:"required,min=10,max=2000"`
}

// CreateAllowlistEntryRequest adds an account manually from F4 (US63).
type CreateAllowlistEntryRequest struct {
	Platform          string `json:"platform" validate:"required,max=64"`
	PlatformAccountID string `json:"platform_account_id" validate:"required,max=255"`
	Handle            string `json:"handle" validate:"required,max=255"`
	Category          string `json:"category" validate:"required,oneof=ngo newsroom campaign_group government union other self_exclusion"`
	Reason            string `json:"reason" validate:"required,min=10,max=2000"`
}

// UpdateAllowlistEntryRequest edits an active entry (US63).
type UpdateAllowlistEntryRequest struct {
	Category *string `json:"category" validate:"omitempty,oneof=ngo newsroom campaign_group government union other self_exclusion"`
	Reason   *string `json:"reason" validate:"omitempty,min=10,max=2000"`
}

// RemoveAllowlistEntryRequest withdraws protection (US63).
//
// The reason is required: removing an organisation's protection is the action
// that lets the detector flag it again, and US63 requires it to be logged.
type RemoveAllowlistEntryRequest struct {
	Reason string `json:"reason" validate:"required,min=10,max=2000"`
}

// AllowlistActionResult reports what an allowlist change actually did.
type AllowlistActionResult struct {
	AccountsAdded    int      `json:"accounts_added"`
	NetworksAffected int      `json:"networks_affected"`
	Handles          []string `json:"handles"`
	// ExportedReportsAffected names networks a report was already generated
	// from. A PDF citing accounts since allowlisted is already in someone's
	// inbox and cannot be recalled; surfacing the exposure is the most the
	// system can do about it.
	ExportedReportsAffected []string `json:"exported_reports_affected,omitempty"`
	Note                    string   `json:"note,omitempty"`
}

// CommonPhrase is one entry of the text exclusion list (PRD 10.5.2.2).
type CommonPhrase struct {
	ID        string    `json:"id"`
	Phrase    string    `json:"phrase"`
	Category  string    `json:"category"`
	Notes     *string   `json:"notes"`
	CreatedAt time.Time `json:"created_at"`
}

// CreateCommonPhraseRequest adds a phrase the duplication signal must ignore.
type CreateCommonPhraseRequest struct {
	Phrase   string  `json:"phrase" validate:"required,min=3,max=1000"`
	Category string  `json:"category" validate:"required,oneof=slogan hashtag policy_name press_release other"`
	Notes    *string `json:"notes" validate:"omitempty,max=2000"`
}

// --- Detector settings (US62) ---

// DetectorSettingsView is the F4 detector configuration payload.
type DetectorSettingsView struct {
	WindowDays      int `json:"window_days"`
	BinWidthSeconds int `json:"bin_width_seconds"`

	NullModelAlpha float64 `json:"null_model_alpha"`
	DupThreshold   float64 `json:"dup_threshold"`
	SemThreshold   float64 `json:"sem_threshold"`
	MinPostLength  int     `json:"min_post_length"`

	EdgeThreshold      float64 `json:"edge_threshold"`
	MinSignalFamilies  int     `json:"min_signal_families"`
	KCore              int     `json:"k_core"`
	LeidenResolution   float64 `json:"leiden_resolution"`
	MinClusterSize     int     `json:"min_cluster_size"`
	MinInternalDensity float64 `json:"min_internal_density"`

	BetaTime   float64 `json:"beta_time"`
	BetaText   float64 `json:"beta_text"`
	BetaAmp    float64 `json:"beta_amp"`
	BetaMeta   float64 `json:"beta_meta"`
	BetaStruct float64 `json:"beta_struct"`

	ProvenanceHalfLifeHours int `json:"provenance_half_life_hours"`

	AnchorShare     float64 `json:"anchor_share"`
	MinClaimPosts   int     `json:"min_claim_posts"`
	MinLinkStrength float64 `json:"min_link_strength"`

	HighScoreCutoff     float64 `json:"high_score_cutoff"`
	HighBreadthCutoff   int     `json:"high_breadth_cutoff"`
	MediumScoreCutoff   float64 `json:"medium_score_cutoff"`
	MediumBreadthCutoff int     `json:"medium_breadth_cutoff"`

	CadenceHours        int     `json:"cadence_hours"`
	CandidateCap        int     `json:"candidate_cap"`
	RecurrenceThreshold float64 `json:"recurrence_threshold"`

	VelocityTriggerThreshold float64 `json:"velocity_trigger_threshold"`
	VelocityTriggerEnabled   bool    `json:"velocity_trigger_enabled"`

	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy *string   `json:"updated_by"`

	// SelfExclusionCount is how many accounts are excluded as the city's own
	// comms estate. US62 lists the self-exclusion account list among the
	// controls; it is managed through the allowlist under its own category, and
	// this is the pointer to it.
	SelfExclusionCount int64 `json:"self_exclusion_count"`
}

// UpdateDetectorSettingsRequest is the body of PUT /settings/detector.
//
// Every field is a pointer so an omitted parameter keeps its stored value: a
// screen that saves one threshold must not silently reset the other
// twenty-nine to whatever its form defaulted to. Range validation lives in
// models.CISDetectorSettings.Validate rather than in struct tags, because two of
// the constraints are cross-field and a tag cannot see a sibling.
type UpdateDetectorSettingsRequest struct {
	WindowDays      *int `json:"window_days"`
	BinWidthSeconds *int `json:"bin_width_seconds"`

	NullModelAlpha *float64 `json:"null_model_alpha"`
	DupThreshold   *float64 `json:"dup_threshold"`
	SemThreshold   *float64 `json:"sem_threshold"`
	MinPostLength  *int     `json:"min_post_length"`

	EdgeThreshold      *float64 `json:"edge_threshold"`
	MinSignalFamilies  *int     `json:"min_signal_families"`
	KCore              *int     `json:"k_core"`
	LeidenResolution   *float64 `json:"leiden_resolution"`
	MinClusterSize     *int     `json:"min_cluster_size"`
	MinInternalDensity *float64 `json:"min_internal_density"`

	BetaTime   *float64 `json:"beta_time"`
	BetaText   *float64 `json:"beta_text"`
	BetaAmp    *float64 `json:"beta_amp"`
	BetaMeta   *float64 `json:"beta_meta"`
	BetaStruct *float64 `json:"beta_struct"`

	ProvenanceHalfLifeHours *int `json:"provenance_half_life_hours"`

	AnchorShare     *float64 `json:"anchor_share"`
	MinClaimPosts   *int     `json:"min_claim_posts"`
	MinLinkStrength *float64 `json:"min_link_strength"`

	HighScoreCutoff     *float64 `json:"high_score_cutoff"`
	HighBreadthCutoff   *int     `json:"high_breadth_cutoff"`
	MediumScoreCutoff   *float64 `json:"medium_score_cutoff"`
	MediumBreadthCutoff *int     `json:"medium_breadth_cutoff"`

	CadenceHours        *int     `json:"cadence_hours"`
	CandidateCap        *int     `json:"candidate_cap"`
	RecurrenceThreshold *float64 `json:"recurrence_threshold"`

	VelocityTriggerThreshold *float64 `json:"velocity_trigger_threshold"`
	VelocityTriggerEnabled   *bool    `json:"velocity_trigger_enabled"`
}

// SettingHistoryEntry is one recorded configuration change (US62).
type SettingHistoryEntry struct {
	ID        string    `json:"id"`
	Key       string    `json:"key"`
	FromValue *string   `json:"from_value"`
	ToValue   string    `json:"to_value"`
	ChangedBy *string   `json:"changed_by"`
	CreatedAt time.Time `json:"created_at"`
}

// --- Detection runs (PRD 10.5.8) ---

// TriggerDetectionRequest asks for an on-demand run.
type TriggerDetectionRequest struct {
	// ClaimIDs is the run scope. One claim is the default, invoked from a claim
	// page; several make it a topic batch.
	ClaimIDs []string `json:"claim_ids" validate:"required,min=1,max=200,dive,uuid"`
}

// DetectionRunView is one detection run (US62's run history).
type DetectionRunView struct {
	RunID         string `json:"run_id"`
	Status        string `json:"status"`
	TriggerSource string `json:"trigger_source"`

	ScopeClaimIDs []string  `json:"scope_claim_ids"`
	WindowStart   time.Time `json:"window_start"`
	WindowEnd     time.Time `json:"window_end"`

	Truncated          bool     `json:"truncated"`
	CandidatesCount    int      `json:"candidates_count"`
	SignalsUnavailable []string `json:"signals_unavailable"`
	// ConfidenceCappedAtMedium is rule 4's consequence, stated. It is the
	// answer to "why is everything Medium this week?", which is a question
	// about runs rather than about networks.
	ConfidenceCappedAtMedium bool `json:"confidence_capped_at_medium"`

	NetworkCount  int64 `json:"network_count"`
	OfftopicCount int64 `json:"offtopic_count"`

	RandomSeed  *int64     `json:"random_seed"`
	StartedAt   time.Time  `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
	Error       *string    `json:"error,omitempty"`

	// Parameters is the configuration in force when the run executed, copied
	// into the run rather than looked up now. US62: changing a parameter must
	// never retroactively alter a stored detection.
	Parameters any `json:"parameters,omitempty"`
}

// TriggerDetectionResponse acknowledges an on-demand run request.
type TriggerDetectionResponse struct {
	RunID    *string  `json:"run_id"`
	Status   string   `json:"status"`
	ClaimIDs []string `json:"claim_ids"`
	Message  string   `json:"message"`
}

// --- Off-topic clusters and dismissals (US62, PRD 10.9.3) ---

// OfftopicClusterView is one cluster the relevance gate rejected.
//
// Never surfaced in the network list and never exported. This read-only view is
// the entire reason PRD 10.5.1a retains them: a rising off-topic rate is the
// signal that omega_min or the candidate scope needs recalibration.
type OfftopicClusterView struct {
	ClusterID      string    `json:"cluster_id"`
	RunID          string    `json:"run_id"`
	ClaimID        string    `json:"claim_id"`
	ClaimStatement *string   `json:"claim_statement"`
	FailedTest     string    `json:"failed_test"`
	OverlapRatio   float64   `json:"overlap_ratio"`
	AnchoringShare float64   `json:"anchoring_share"`
	AccountCount   int       `json:"account_count"`
	PostCount      int       `json:"post_count"`
	Signals        any       `json:"signals,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

// OfftopicRate is one run's surfaced-vs-rejected ratio (US62).
type OfftopicRate struct {
	RunID         string    `json:"run_id"`
	StartedAt     time.Time `json:"started_at"`
	SurfacedCount int64     `json:"surfaced_count"`
	OfftopicCount int64     `json:"offtopic_count"`
	Rate          float64   `json:"rate"`
	FailedTests   []string  `json:"failed_tests"`
}

// DismissalView is one recorded false-positive dismissal (PRD 10.9.3).
type DismissalView struct {
	ID            string    `json:"id"`
	NetworkID     string    `json:"network_id"`
	NetworkLabel  *string   `json:"network_label"`
	Reason        string    `json:"reason"`
	UserID        *string   `json:"user_id"`
	CreatedAt     time.Time `json:"created_at"`
	SignalProfile any       `json:"signal_profile,omitempty"`
}

// DismissalSummary is the aggregate view PRD 10.9.3 requires so the team can
// identify a systematically over-triggering signal and recalibrate beta_k or
// the thresholds in F4.
type DismissalSummary struct {
	WindowDays  int   `json:"window_days"`
	Confirmed   int64 `json:"confirmed"`
	ActionTaken int64 `json:"action_taken"`
	Dismissed   int64 `json:"dismissed"`

	// Precision is confirmed+action_taken over all three. PRD 10.9.3 sets a
	// recommended operational target above 0.85 on a rolling 90-day basis, and
	// deliberately makes recall secondary: "a missed network costs a missed
	// referral; a false positive costs a government publicly implying that
	// residents are bots."
	Precision       *float64 `json:"precision"`
	PrecisionTarget float64  `json:"precision_target"`
	MeetsTarget     *bool    `json:"meets_target"`

	// MeanSignalScores averages each metric across dismissals. A signal that is
	// consistently high on rejected networks is the one over-triggering.
	MeanSignalScores map[string]float64 `json:"mean_signal_scores,omitempty"`
	SampleSize       int                `json:"sample_size"`
	Note             string             `json:"note,omitempty"`
}

// --- Reports and bundles (US58, US59, US60) ---

// GenerateReportRequest is US59's pre-generation modal.
type GenerateReportRequest struct {
	ReportType string `json:"report_type" validate:"required,oneof=platform_referral internal_briefing"`

	// Section toggles. IncludeAccountAnnex is honoured only for an internal
	// briefing: US59 makes the annex mandatory in a Platform referral and
	// non-toggleable, because "a referral without the account list is not
	// actionable".
	IncludeGraph           *bool `json:"include_graph"`
	IncludeContentClusters *bool `json:"include_content_clusters"`
	IncludeAccountAnnex    *bool `json:"include_account_annex"`
	IncludeMethodology     *bool `json:"include_methodology"`

	// RedactAnalystNames removes the generating user's name from the cover and
	// the review history.
	RedactAnalystNames *bool `json:"redact_analyst_names"`
}

// ReportSections records what a generated report contained.
type ReportSections struct {
	Graph           bool `json:"graph"`
	ContentClusters bool `json:"content_clusters"`
	AccountAnnex    bool `json:"account_annex"`
	Methodology     bool `json:"methodology"`
}

// ReportView is one generated report (US58).
type ReportView struct {
	ID         string `json:"id"`
	NetworkID  string `json:"network_id"`
	RunID      string `json:"run_id"`
	ReportType string `json:"report_type"`

	FileName   string `json:"file_name"`
	FileSHA256 string `json:"file_sha256"`
	FileSize   int64  `json:"file_size_bytes"`

	Sections       ReportSections `json:"sections"`
	RedactAnalysts bool           `json:"redact_analyst_names"`

	// Chain of custody (PRD 10.8 item 10).
	SnapshotID     *string `json:"snapshot_id"`
	SnapshotSHA256 *string `json:"snapshot_sha256"`
	AuditID        *string `json:"audit_id"`

	GeneratedBy *string   `json:"generated_by"`
	GeneratedAt time.Time `json:"generated_at"`
	DownloadURL string    `json:"download_url"`
}

// AuditLogEntry is one export audit record (US64).
type AuditLogEntry struct {
	ID         string    `json:"id"`
	ObjectType string    `json:"object_type"`
	ObjectID   string    `json:"object_id"`
	NetworkID  string    `json:"network_id"`
	RunID      *string   `json:"run_id"`
	ExportType string    `json:"export_type"`
	UserID     *string   `json:"user_id"`
	UserName   *string   `json:"user_name"`
	Settings   any       `json:"settings,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}
