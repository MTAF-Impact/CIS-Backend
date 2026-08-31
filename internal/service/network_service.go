package service

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/cis/cis-backend/internal/detector"
	"github.com/cis/cis-backend/internal/dto"
	"github.com/cis/cis-backend/internal/models"
	"github.com/cis/cis-backend/internal/pkg/apperr"
	"github.com/cis/cis-backend/internal/repository"
)

// NetworkService assembles the F5 Coordinated-Network Detector payloads.
//
// It reads what the pipeline detected and owns what humans decide about it: the
// review workflow (US52). It never computes a coordination score, a confidence
// band or a signal breadth — those are the detector's, and recomputing one here
// would produce a number that disagrees with the PDF it was printed in.
type NetworkService struct {
	networks *repository.NetworkRepository
	claims   *repository.ClaimRepository
	policies *repository.PolicyRepository
	settings *SettingService
}

// NewNetworkService constructs a NetworkService.
func NewNetworkService(
	networks *repository.NetworkRepository,
	claims *repository.ClaimRepository,
	policies *repository.PolicyRepository,
	settings *SettingService,
) *NetworkService {
	return &NetworkService{networks: networks, claims: claims, policies: policies, settings: settings}
}

// ListNetworksQuery is the normalized input for the F5 list (US43-US48).
type ListNetworksQuery struct {
	Status            string
	ConfidenceBands   []string
	ShowLowConfidence bool
	ClaimIDs          []uuid.UUID
	TopicIDs          []uuid.UUID
	PolicyIDs         []uuid.UUID
	Search            string
	DetectedFrom      *time.Time
	DetectedTo        *time.Time
	SortBy            string
	Page              int
	Limit             int
}

// NetworkListResponse is the F5 main page payload.
type NetworkListResponse struct {
	Networks     []dto.NetworkCard `json:"networks"`
	StatusCounts map[string]int64  `json:"status_counts"`
	// LowConfidenceShown echoes the toggle back so the client is never in doubt
	// about which set it is looking at.
	LowConfidenceShown bool   `json:"low_confidence_shown"`
	AppliedSort        string `json:"applied_sort"`
}

// List returns a page of networks (US43-US48).
func (s *NetworkService) List(ctx context.Context, q ListNetworksQuery) (*NetworkListResponse, int64, dto.PageParams, error) {
	page := dto.NormalizePage(q.Page, q.Limit)

	if q.Status != "" && q.Status != "all" && !models.IsValidNetworkReviewStatus(q.Status) {
		return nil, 0, page, apperr.BadRequest(
			"status must be one of: all, %s", strings.Join(models.ValidNetworkReviewStatuses, ", "))
	}
	for _, band := range q.ConfidenceBands {
		if !models.IsValidConfidenceBand(band) {
			return nil, 0, page, apperr.BadRequest("confidence must be one of: low, medium, high")
		}
	}

	filter := repository.NetworkFilter{
		ReviewStatus:         q.Status,
		ConfidenceBands:      q.ConfidenceBands,
		IncludeLowConfidence: q.ShowLowConfidence,
		ClaimIDs:             q.ClaimIDs,
		TopicIDs:             q.TopicIDs,
		PolicyIDs:            q.PolicyIDs,
		Search:               q.Search,
		DetectedFrom:         q.DetectedFrom,
		DetectedTo:           q.DetectedTo,
		SortBy:               q.SortBy,
		Limit:                page.Limit,
		Offset:               page.Offset(),
	}

	rows, err := s.networks.ListNetworks(ctx, filter)
	if err != nil {
		return nil, 0, page, translatePipelineErr(err, "could not load coordinated networks")
	}
	total, err := s.networks.CountNetworks(ctx, filter)
	if err != nil {
		return nil, 0, page, translatePipelineErr(err, "could not count coordinated networks")
	}
	counts, err := s.networks.CountNetworksByStatus(ctx, filter)
	if err != nil {
		return nil, 0, page, translatePipelineErr(err, "could not count networks by status")
	}

	cards := make([]dto.NetworkCard, 0, len(rows))
	for _, row := range rows {
		cards = append(cards, buildNetworkCard(row))
	}

	sortBy := q.SortBy
	if sortBy == "" {
		sortBy = repository.NetworkSortScore
	}

	return &NetworkListResponse{
		Networks:           cards,
		StatusCounts:       counts,
		LowConfidenceShown: q.ShowLowConfidence,
		AppliedSort:        sortBy,
	}, total, page, nil
}

// buildNetworkCard converts one row into the US46 card.
func buildNetworkCard(row repository.NetworkRow) dto.NetworkCard {
	card := dto.NetworkCard{
		ID:                row.ID.String(),
		Label:             row.Label,
		CoordinationScore: row.CoordinationScore,
		ConfidenceBand:    row.ConfidenceBand,
		SignalBreadth:     row.SignalBreadth,
		ReviewStatus:      row.ReviewStatus,
		AccountCount:      row.AccountCount,
		PostCount:         row.PostCount,
		Platforms:         row.Platforms,
		DetectedAt:        row.DetectedAt,
		LowConfidence:     row.ConfidenceBand == models.ConfidenceLow,
		FromTruncatedRun:  row.RunTruncated,
	}
	if card.Platforms == nil {
		card.Platforms = []string{}
	}
	if row.PrimaryClaimID != nil && row.PrimaryClaimStatement != nil {
		card.PrimaryClaim = buildClaimRef(row)
	}

	card.Recurrence = dto.RecurrenceInfo{
		Count:        maxInt(row.RecurrenceCount, 1),
		FirstSeenAt:  row.FirstSeenAt,
		IsRecurrence: row.ParentNetworkID != nil,
	}
	return card
}

func buildClaimRef(row repository.NetworkRow) *dto.NetworkClaimRef {
	ref := &dto.NetworkClaimRef{
		ClaimID:        row.PrimaryClaimID.String(),
		ClaimStatement: *row.PrimaryClaimStatement,
		IsPrimary:      true,
	}
	if row.OverlapRatio != nil {
		ref.OverlapRatio = *row.OverlapRatio
	}
	if row.AnchoringShare != nil {
		ref.AnchoringShare = *row.AnchoringShare
	}
	if row.ClaimClusterPostCnt != nil {
		ref.ClaimClusterPosts = *row.ClaimClusterPostCnt
	}
	if row.PassedRelevanceGate != nil {
		ref.PassedRelevanceGate = *row.PassedRelevanceGate
	}
	if row.PrimaryTopicID != nil && row.PrimaryTopicName != nil {
		ref.Topic = &dto.TopicRef{ID: row.PrimaryTopicID.String(), Name: *row.PrimaryTopicName}
	}
	return ref
}

// Detail builds the US49/US50 network detail page.
func (s *NetworkService) Detail(ctx context.Context, id uuid.UUID) (*dto.NetworkDetail, error) {
	row, err := s.networks.FindNetworkByID(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, apperr.NotFound("coordinated network not found")
		}
		return nil, translatePipelineErr(err, "could not load coordinated network")
	}

	// A suppressed network is not reachable through any listing surface
	// (PRD 10.6.3 rule 3), and a direct link to one must not become the way
	// around that. It is refused with the reason stated rather than a bare 404,
	// because "we suppressed this because your team declared these accounts
	// legitimate" is information the analyst needs, and a 404 would read as
	// data loss.
	if row.AllowlistSuppressed {
		return nil, apperr.Forbidden(
			"this network is suppressed: at least %.0f%% of its members are on the declared-coordination allowlist, "+
				"so it is not surfaced on any page (PRD 10.6.3)", models.AllowlistSuppressionShare*100)
	}

	settings, err := s.settings.DetectorSettings(ctx)
	if err != nil {
		return nil, err
	}

	detail := &dto.NetworkDetail{
		NetworkCard: buildNetworkCard(*row),
		Run:         buildRunContext(*row),
		Disclaimer:  detector.Disclaimer,
	}

	links, err := s.networks.ListClaimLinks(ctx, id)
	if err != nil {
		return nil, translatePipelineErr(err, "could not load linked claims")
	}
	detail.LinkedClaims = buildClaimRefs(links)

	// Linked policies resolve transitively through the linked claims: a network
	// amplifies a claim, and the claim is correlated with policies. US49 links
	// out to the existing F2 detail pages rather than duplicating any policy UI
	// inside F5.
	policyIDs, err := s.policyIDsForClaims(ctx, links)
	if err != nil {
		return nil, err
	}
	refs, err := resolvePolicyRefs(ctx, s.policies, policyIDs)
	if err != nil {
		return nil, err
	}
	detail.LinkedPolicies = refs

	detail.WhyFlagged = buildWhyFlagged(*row, links, *settings)

	if row.ReviewedAt != nil {
		review := &dto.NetworkReview{Status: row.ReviewStatus, ReviewedAt: row.ReviewedAt}
		if row.ReviewReason != nil {
			review.Reason = *row.ReviewReason
		}
		if row.ReviewedBy != nil {
			by := row.ReviewedBy.String()
			review.ReviewedBy = &by
		}
		detail.Review = review
	}

	// Prior anchoring claims. PRD 10.5.1: a recurrence inherits a network's
	// history but not its relevance, and both the detail page and the report
	// must state the current primary claim AND the prior anchoring claims.
	if row.ParentNetworkID != nil {
		chain, err := s.networks.ListRecurrenceChain(ctx, id)
		if err != nil {
			return nil, translatePipelineErr(err, "could not load recurrence history")
		}
		detail.Recurrence.PriorClaims = buildPriorAnchors(chain)
	}

	detail.Export = evaluateExportEligibility(*row)
	return detail, nil
}

// buildRunContext copies the run-level facts that change how a network must be
// read (PRD 10.5.1, 10.6.3 rule 4).
func buildRunContext(row repository.NetworkRow) dto.RunContext {
	unavailable := []string(row.RunSignalsUnavailable)
	if unavailable == nil {
		unavailable = []string{}
	}
	capped := row.RunTruncated || len(unavailable) >= 2

	ctxOut := dto.RunContext{
		RunID:                    row.RunID.String(),
		TriggerSource:            row.RunTriggerSource,
		WindowStart:              row.RunWindowStart,
		WindowEnd:                row.RunWindowEnd,
		CompletedAt:              row.RunCompletedAt,
		Truncated:                row.RunTruncated,
		CandidatesCount:          row.RunCandidatesCount,
		SignalsUnavailable:       unavailable,
		ConfidenceCappedAtMedium: capped,
	}
	if row.RunTruncated {
		ctxOut.TruncationNote = detector.TruncationNote
	}
	return ctxOut
}

func buildClaimRefs(links []repository.ClaimLinkRow) []dto.NetworkClaimRef {
	out := make([]dto.NetworkClaimRef, 0, len(links))
	for _, l := range links {
		ref := dto.NetworkClaimRef{
			ClaimID:             l.ClaimID.String(),
			ClaimStatement:      l.ClaimStatement,
			ClaimType:           models.NormalizeClaimType(l.ClaimType),
			IsPrimary:           l.IsPrimaryClaim,
			OverlapRatio:        l.OverlapRatio,
			AnchoringShare:      l.AnchoringShare,
			ClaimClusterPosts:   l.ClaimClusterPostCnt,
			PassedRelevanceGate: l.PassedRelevanceGate,
		}
		if l.TopicID != nil && l.TopicName != nil {
			ref.Topic = &dto.TopicRef{ID: l.TopicID.String(), Name: *l.TopicName}
		}
		out = append(out, ref)
	}
	return out
}

func buildPriorAnchors(chain []repository.AncestorLink) []dto.PriorAnchorRef {
	out := make([]dto.PriorAnchorRef, 0, len(chain))
	for _, a := range chain {
		ref := dto.PriorAnchorRef{
			NetworkID:         a.NetworkID.String(),
			Label:             a.Label,
			DetectedAt:        a.DetectedAt,
			ConfidenceBand:    a.ConfidenceBand,
			CoordinationScore: a.Score,
			ClaimStatement:    a.ClaimStatement,
		}
		if a.ClaimID != nil {
			id := a.ClaimID.String()
			ref.ClaimID = &id
		}
		out = append(out, ref)
	}
	return out
}

// buildWhyFlagged assembles the US50 panel.
//
// Its hard constraint is the same as US23's: the composite score must never be
// displayed without access to this breakdown. So the panel carries every
// metric's score AND its raw counts AND a plain-language method sentence, the
// confidence rule that was applied, the families that could not be measured,
// and the claim-relevance block — not a subset chosen for brevity.
func buildWhyFlagged(
	row repository.NetworkRow, links []repository.ClaimLinkRow, settings models.CISDetectorSettings,
) dto.WhyFlagged {
	unavailable := []string(row.RunSignalsUnavailable)
	if unavailable == nil {
		unavailable = []string{}
	}

	scores := map[string]float64{
		detector.SignalSY: row.SY,
		detector.SignalDU: row.DU,
		detector.SignalCO: row.CO,
		detector.SignalPR: row.PR,
		detector.SignalAU: row.AU,
	}
	rawCounts := decodeJSONMap(row.RawCounts)

	signals := make([]dto.SignalDetail, 0, len(detector.SignalCatalogue))
	for _, meta := range detector.SignalCatalogue {
		detail := dto.SignalDetail{
			Code:      meta.Code,
			Name:      meta.Name,
			Score:     scores[meta.Code],
			Method:    meta.Method,
			Weight:    meta.Weight,
			Available: true,
		}
		// A metric whose underlying family was unavailable is reported as
		// unavailable rather than as a score of zero. Conflating "we could not
		// measure this" with "we measured this and it was nil" is the same
		// error PRD 10.5.2.4 warns against for missing metadata fields, and it
		// biases every reading downward.
		for _, family := range meta.Families {
			for _, missing := range unavailable {
				if family == missing {
					detail.Available = false
				}
			}
		}
		if counts, ok := rawCounts[strings.ToLower(meta.Code)]; ok {
			detail.RawCounts = counts
		}
		signals = append(signals, detail)
	}

	out := dto.WhyFlagged{
		CoordinationScore:      row.CoordinationScore,
		Signals:                signals,
		SignalsUnavailable:     labelFamilies(unavailable),
		InternalDensity:        row.InternalDens,
		Conductance:            row.Conductance,
		ComparisonAccountCount: row.ComparisonAccountCount,
		KnownLimitations:       detector.KnownLimitations,
		Confidence: dto.ConfidenceExplanation{
			Band:          row.ConfidenceBand,
			SignalBreadth: row.SignalBreadth,
			Rule:          describeBandRule(row, settings),
			CappedByRun:   row.RunTruncated || len(unavailable) >= 2,
			Note:          detector.BreadthGuardNote,
		},
		ClaimRelevance: dto.ClaimRelevanceBlock{
			SecondaryClaims:          []dto.NetworkClaimRef{},
			AnchorShareThreshold:     settings.AnchorShare,
			MinClaimPostsThreshold:   settings.MinClaimPosts,
			MinLinkStrengthThreshold: settings.MinLinkStrength,
		},
	}

	for _, ref := range buildClaimRefs(links) {
		r := ref
		if r.IsPrimary {
			out.ClaimRelevance.PrimaryClaim = &r
			continue
		}
		out.ClaimRelevance.SecondaryClaims = append(out.ClaimRelevance.SecondaryClaims, r)
	}
	return out
}

// describeBandRule writes out the condition that produced the band, so US50's
// panel can state it rather than leave a reader to look it up.
func describeBandRule(row repository.NetworkRow, s models.CISDetectorSettings) string {
	switch row.ConfidenceBand {
	case models.ConfidenceHigh:
		return fmt.Sprintf("High: CoordinationScore >= %g and SignalBreadth >= %d", s.HighScoreCutoff, s.HighBreadthCutoff)
	case models.ConfidenceMedium:
		if row.RunTruncated || len(row.RunSignalsUnavailable) >= 2 {
			return fmt.Sprintf(
				"Medium: capped by the run. %s Score %.1f with SignalBreadth %d would otherwise be banded on its own merits.",
				detector.ConfidenceCapNote, row.CoordinationScore, row.SignalBreadth)
		}
		return fmt.Sprintf("Medium: CoordinationScore >= %g and SignalBreadth >= %d", s.MediumScoreCutoff, s.MediumBreadthCutoff)
	default:
		return fmt.Sprintf(
			"Low: did not reach CoordinationScore >= %g with SignalBreadth >= %d",
			s.MediumScoreCutoff, s.MediumBreadthCutoff)
	}
}

func labelFamilies(keys []string) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, detector.FamilyLabel(k))
	}
	return out
}

// evaluateExportEligibility applies US58's gate and explains the outcome.
//
// Written as an ALLOWLIST of statuses plus a band check, both server-side. The
// natural denylist — "not unreviewed" — would permit exporting a network the
// team has already examined and concluded was organic, which is a government
// submitting a referral about residents it itself cleared. This predicate is
// where that either happens or does not.
func evaluateExportEligibility(row repository.NetworkRow) dto.ExportEligibility {
	out := dto.ExportEligibility{AllowedStatuses: models.ReportableNetworkStatuses}

	if !models.IsReportableNetworkStatus(row.ReviewStatus) {
		if row.ReviewStatus == models.NetworkStatusDismissedFP {
			out.Reason = "This network was assessed and dismissed as a false positive. " +
				"Exporting it would submit a referral about accounts the team has already concluded were not coordinating."
			return out
		}
		out.Reason = "A network cannot be exported while unreviewed. " +
			"An unreviewed export is an unreviewed accusation (PRD 10.9.1 rule 4)."
		return out
	}

	if row.ConfidenceBand == models.ConfidenceLow {
		out.Reason = "Reports may only be generated for networks at Medium or High confidence."
		return out
	}

	out.Allowed = true
	return out
}

func (s *NetworkService) policyIDsForClaims(ctx context.Context, links []repository.ClaimLinkRow) ([]uuid.UUID, error) {
	seen := map[uuid.UUID]struct{}{}
	var out []uuid.UUID
	for _, l := range links {
		ids, err := s.claims.ListPolicyIDsForClaim(ctx, l.ClaimID, nil)
		if err != nil {
			return nil, apperr.Internal("could not resolve linked policies").Wrap(err)
		}
		for _, id := range ids {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	return out, nil
}

// UpdateStatus records a human's assessment of a network (US52).
//
// Three things happen that do not happen for a claim status change:
//
//  1. The reason is mandatory and at least 20 characters. A network assessment
//     is an evidentiary judgment about real accounts, and one without a stated
//     reason teaches the allowlist nothing.
//  2. The change appends to an immutable log rather than overwriting a note.
//  3. The network's full signal profile is COPIED into that log entry. PRD
//     10.9.3 requires dismissals to carry their signal profile and to be
//     reviewable in aggregate; a later run can recompute those scores, so
//     joining at read time would give a drifting answer.
func (s *NetworkService) UpdateStatus(
	ctx context.Context, id uuid.UUID, req dto.UpdateNetworkStatusRequest, userID *uuid.UUID,
) (*dto.NetworkStatusResponse, error) {
	if !models.IsValidNetworkReviewStatus(req.Status) {
		return nil, apperr.Unprocessable(
			"status must be one of: %s", strings.Join(models.ValidNetworkReviewStatuses, ", "))
	}

	reason := strings.TrimSpace(req.Reason)
	if len([]rune(reason)) < models.NetworkStatusReasonMinLength {
		return nil, apperr.Unprocessable(
			"a reason of at least %d characters is required to change a network's review status (US52)",
			models.NetworkStatusReasonMinLength)
	}

	row, err := s.networks.FindNetworkByID(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, apperr.NotFound("coordinated network not found")
		}
		return nil, translatePipelineErr(err, "could not load coordinated network")
	}

	profile := models.MustJSONB(map[string]any{
		"coordination_score":  row.CoordinationScore,
		"sy":                  row.SY,
		"du":                  row.DU,
		"co":                  row.CO,
		"pr":                  row.PR,
		"au":                  row.AU,
		"signal_breadth":      row.SignalBreadth,
		"confidence_band":     row.ConfidenceBand,
		"account_count":       row.AccountCount,
		"post_count":          row.PostCount,
		"internal_density":    row.InternalDens,
		"conductance":         row.Conductance,
		"signals_unavailable": []string(row.RunSignalsUnavailable),
		"run_truncated":       row.RunTruncated,
		"run_id":              row.RunID.String(),
		"captured_at":         time.Now().UTC(),
	})

	review, err := s.networks.UpsertReview(ctx, id, row.ReviewStatus, req.Status, reason, profile, userID)
	if err != nil {
		return nil, apperr.Internal("could not record the network review").Wrap(err)
	}

	res := &dto.NetworkStatusResponse{
		NetworkID:  id.String(),
		FromStatus: row.ReviewStatus,
		Status:     review.Status,
		Reason:     review.Reason,
		ReviewedAt: review.ReviewedAt,
	}
	if review.ReviewedBy != nil {
		by := review.ReviewedBy.String()
		res.ReviewedBy = &by
	}
	return res, nil
}

// ReviewLog returns a network's status history (US52's record, US59's internal
// briefing).
func (s *NetworkService) ReviewLog(ctx context.Context, id uuid.UUID, limit int) ([]dto.NetworkReviewLogEntry, error) {
	rows, err := s.networks.ListReviewLog(ctx, id, limit)
	if err != nil {
		return nil, apperr.Internal("could not load the network review log").Wrap(err)
	}

	out := make([]dto.NetworkReviewLogEntry, 0, len(rows))
	for _, r := range rows {
		entry := dto.NetworkReviewLogEntry{
			ID:         r.ID.String(),
			FromStatus: r.FromStatus,
			ToStatus:   r.ToStatus,
			Reason:     r.Reason,
			CreatedAt:  r.CreatedAt,
		}
		if r.UserID != nil {
			id := r.UserID.String()
			entry.UserID = &id
		}
		if len(r.SignalProfile) > 0 {
			entry.SignalProfile = json.RawMessage(r.SignalProfile)
		}
		out = append(out, entry)
	}
	return out, nil
}

// Graph builds the US51 force-directed graph payload.
func (s *NetworkService) Graph(ctx context.Context, id uuid.UUID) (*dto.NetworkGraph, error) {
	if err := s.assertVisible(ctx, id); err != nil {
		return nil, err
	}

	accounts, _, err := s.networks.ListNetworkAccounts(ctx, id, "", repository.AccountSortCentrality, "", 0, 0)
	if err != nil {
		return nil, translatePipelineErr(err, "could not load network accounts")
	}
	edges, err := s.networks.ListEdges(ctx, id)
	if err != nil {
		return nil, translatePipelineErr(err, "could not load network edges")
	}

	graph := &dto.NetworkGraph{
		Nodes:          make([]dto.GraphNode, 0, len(accounts)),
		Edges:          make([]dto.GraphEdge, 0, len(edges)),
		TotalNodeCount: len(accounts),
	}

	// Above the legibility limit US51 asks for the k-core rather than the whole
	// graph, and requires the reduction to be noted. Degree centrality is the
	// available proxy for core-ness at this layer: the pipeline computed the
	// k-core when it built the graph, so what is being trimmed here is the
	// rendering, not the finding.
	keep := accounts
	if len(accounts) > detector.GraphLegibilityLimit {
		sorted := make([]repository.NetworkAccountRow, len(accounts))
		copy(sorted, accounts)
		sort.SliceStable(sorted, func(i, j int) bool {
			return sorted[i].DegreeCentrality > sorted[j].DegreeCentrality
		})
		keep = sorted[:detector.GraphLegibilityLimit]
		graph.Reduced = true
		graph.ReductionNote = fmt.Sprintf(
			"This network has %d accounts. The graph renders the %d most connected of them; "+
				"the remaining %d are listed in full in the account annex.",
			len(accounts), detector.GraphLegibilityLimit, len(accounts)-detector.GraphLegibilityLimit)
	}

	visible := make(map[uuid.UUID]struct{}, len(keep))
	for _, a := range keep {
		visible[a.AccountID] = struct{}{}
		if a.MembershipRole == models.MembershipComparison {
			graph.ComparisonCount++
		} else {
			graph.MemberCount++
		}
		graph.Nodes = append(graph.Nodes, dto.GraphNode{
			AccountID:             a.AccountID.String(),
			Handle:                a.Handle,
			Platform:              a.Platform,
			Role:                  defaultRole(a.MembershipRole),
			DegreeCentrality:      a.DegreeCentrality,
			EigenvectorCentrality: a.EigenvectorCentrality,
			PostsInCluster:        a.PostsInCluster,
			X:                     a.LayoutX,
			Y:                     a.LayoutY,
			Allowlisted:           a.Allowlisted,
		})
	}

	for _, e := range edges {
		if _, ok := visible[e.AccountA]; !ok {
			continue
		}
		if _, ok := visible[e.AccountB]; !ok {
			continue
		}
		graph.Edges = append(graph.Edges, buildGraphEdge(e))
	}
	return graph, nil
}

func buildGraphEdge(e repository.EdgeRow) dto.GraphEdge {
	return dto.GraphEdge{
		Source: e.AccountA.String(),
		Target: e.AccountB.String(),
		Weight: e.WTotal,
		Signals: dto.EdgeSignals{
			Time:   e.WTime,
			Text:   e.WText,
			Amp:    e.WAmp,
			Meta:   e.WMeta,
			Struct: e.WStruct,
		},
		SignalCount: e.SignalCount,
	}
}

func defaultRole(role string) string {
	if role == "" {
		return models.MembershipMember
	}
	return role
}

// Timeline builds the US53 burst chart.
func (s *NetworkService) Timeline(ctx context.Context, id uuid.UUID) (*dto.BurstTimeline, error) {
	row, err := s.visibleNetwork(ctx, id)
	if err != nil {
		return nil, err
	}

	bins, err := s.networks.ListBurstBins(ctx, id)
	if err != nil {
		return nil, translatePipelineErr(err, "could not load the burst timeline")
	}

	out := &dto.BurstTimeline{
		WindowStart: row.RunWindowStart,
		WindowEnd:   row.RunWindowEnd,
		Bins:        make([]dto.BurstBin, 0, len(bins)),
	}
	for _, b := range bins {
		if out.BinWidthSeconds == 0 {
			out.BinWidthSeconds = b.BinWidthSeconds
		}
		if b.IsAnomalous {
			out.AnomalousCount++
		}
		out.Bins = append(out.Bins, dto.BurstBin{
			BinStart:    b.BinStart,
			PostCount:   b.PostCount,
			ZScore:      b.ZScore,
			IsAnomalous: b.IsAnomalous,
		})
	}
	return out, nil
}

// Content builds the US54 representative-content view.
//
// Rendered entirely from the evidence snapshot and never re-fetched live. That
// is not an optimisation: operators delete their own content once a campaign
// concludes, so a live fetch two weeks after detection returns an empty set and
// the duplication claim becomes unverifiable exactly when someone tries to
// verify it.
func (s *NetworkService) Content(ctx context.Context, id uuid.UUID) (*dto.RepresentativeContent, error) {
	if err := s.assertVisible(ctx, id); err != nil {
		return nil, err
	}

	posts, err := s.networks.ListEvidencePosts(ctx, id, nil)
	if err != nil {
		return nil, translatePipelineErr(err, "could not load evidence posts")
	}

	out := &dto.RepresentativeContent{
		Groups:    []dto.DuplicateGroup{},
		Ungrouped: []dto.EvidencePost{},
		Note: "Content is rendered from the evidence snapshot captured at detection time, not re-fetched. " +
			"Posts deleted since capture remain visible and are marked as no longer publicly available.",
	}

	byGroup := map[uuid.UUID]*dto.DuplicateGroup{}
	var order []uuid.UUID

	for _, p := range posts {
		post := buildEvidencePost(p)
		if p.DuplicateGroupID == nil {
			out.Ungrouped = append(out.Ungrouped, post)
			continue
		}
		gid := *p.DuplicateGroupID
		group, ok := byGroup[gid]
		if !ok {
			group = &dto.DuplicateGroup{GroupID: gid.String(), Variants: []dto.EvidencePost{}}
			byGroup[gid] = group
			order = append(order, gid)
		}
		if p.IsCanonical {
			group.CanonicalText = p.CapturedText
		}
		group.Variants = append(group.Variants, post)
	}

	for _, gid := range order {
		g := byGroup[gid]
		g.VariantCount = len(g.Variants)
		if g.CanonicalText == "" && len(g.Variants) > 0 {
			// No row was flagged canonical. Falling back to the first variant
			// keeps the group renderable rather than showing an empty heading.
			g.CanonicalText = g.Variants[0].Text
		}
		out.Groups = append(out.Groups, *g)
	}

	// Largest groups first: US54 asks for "the top duplicate groups", and the
	// size of a group is what makes it representative.
	sort.SliceStable(out.Groups, func(i, j int) bool {
		return out.Groups[i].VariantCount > out.Groups[j].VariantCount
	})
	return out, nil
}

func buildEvidencePost(p repository.EvidencePostRow) dto.EvidencePost {
	return dto.EvidencePost{
		ID:              p.ID.String(),
		AccountID:       p.AccountID.String(),
		Handle:          p.Handle,
		Platform:        p.Platform,
		PostPlatformID:  p.PostPlatformID,
		Text:            p.CapturedText,
		PostedAt:        p.PostedAt,
		CapturedAt:      p.CapturedAt,
		ContentSHA256:   p.ContentSHA256,
		IsCanonical:     p.IsCanonical,
		StillPublic:     p.StillPublic,
		Availability:    detector.Availability(p.StillPublic),
		SharedSpanStart: p.SharedSpanStart,
		SharedSpanEnd:   p.SharedSpanEnd,
	}
}

// AccountsQuery is the normalized input for the US55 annex.
type AccountsQuery struct {
	Role   string
	Search string
	SortBy string
	Page   int
	Limit  int
}

// Accounts returns a page of the account annex (US55).
func (s *NetworkService) Accounts(
	ctx context.Context, id uuid.UUID, q AccountsQuery,
) ([]dto.AccountAnnexRow, int64, dto.PageParams, error) {
	page := dto.NormalizePage(q.Page, q.Limit)
	if err := s.assertVisible(ctx, id); err != nil {
		return nil, 0, page, err
	}

	rows, total, err := s.networks.ListNetworkAccounts(ctx, id, q.Role, q.SortBy, q.Search, page.Limit, page.Offset())
	if err != nil {
		return nil, 0, page, translatePipelineErr(err, "could not load network accounts")
	}

	out := make([]dto.AccountAnnexRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, buildAnnexRow(r))
	}
	return out, total, page, nil
}

func buildAnnexRow(r repository.NetworkAccountRow) dto.AccountAnnexRow {
	row := dto.AccountAnnexRow{
		AccountID:             r.AccountID.String(),
		Handle:                r.Handle,
		Platform:              r.Platform,
		PlatformAccountID:     r.PlatformAccountID,
		CreatedAtPlatform:     r.CreatedAtPlatform,
		PostsInCluster:        r.PostsInCluster,
		DuplicationRate:       r.DuplicationRate,
		MedianInterpostSecs:   r.MedianInterpostSecs,
		CircadianCoverage:     r.CircadianCoverage,
		DegreeCentrality:      r.DegreeCentrality,
		EigenvectorCentrality: r.EigenvectorCentrality,
		Role:                  defaultRole(r.MembershipRole),
		Allowlisted:           r.Allowlisted,
	}
	if len(r.ScoreContribution) > 0 {
		row.ScoreContribution = json.RawMessage(r.ScoreContribution)
	}
	return row
}

// AccountDrawer returns one account's posts and the specific edges that
// connected it to the network (US55).
//
// This endpoint is the implementation of a single sentence: "No account may
// appear in a network without a viewable reason." Everything it returns is that
// reason, made concrete.
func (s *NetworkService) AccountDrawer(ctx context.Context, networkID, accountID uuid.UUID) (*dto.AccountDrawer, error) {
	if err := s.assertVisible(ctx, networkID); err != nil {
		return nil, err
	}

	row, err := s.networks.FindNetworkAccount(ctx, networkID, accountID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, apperr.NotFound("this account is not a member of that network")
		}
		return nil, translatePipelineErr(err, "could not load the account")
	}

	edges, err := s.networks.ListEdgesForAccount(ctx, networkID, accountID)
	if err != nil {
		return nil, translatePipelineErr(err, "could not load the account's edges")
	}

	posts, err := s.networks.ListEvidencePosts(ctx, networkID, nil)
	if err != nil {
		return nil, translatePipelineErr(err, "could not load the account's posts")
	}

	out := &dto.AccountDrawer{
		Account:         buildAnnexRow(*row),
		Posts:           []dto.EvidencePost{},
		ConnectingEdges: make([]dto.GraphEdge, 0, len(edges)),
	}
	for _, p := range posts {
		if p.AccountID == accountID {
			out.Posts = append(out.Posts, buildEvidencePost(p))
		}
	}
	for _, e := range edges {
		out.ConnectingEdges = append(out.ConnectingEdges, buildGraphEdge(e))
	}
	out.Explanation = explainMembership(row.Handle, edges)
	return out, nil
}

// explainMembership renders the account's inclusion in words.
//
// Deliberately phrased as observed behaviour rather than as a verdict: "shares
// N behavioural edges" and never "is part of a bot network". PRD 10.9.1 rule 3
// makes that a hard rule, and the place it is most tempting to break is exactly
// here, where a reader is asking why one specific account was included.
func explainMembership(handle string, edges []repository.EdgeRow) string {
	if len(edges) == 0 {
		return fmt.Sprintf(
			"No retained edges were recorded for %s in this network. "+
				"Without at least one edge there is no measured behavioural relationship to report.", handle)
	}

	families := map[string]int{}
	for _, e := range edges {
		if e.WTime >= 0.25 {
			families[detector.FamilyTime]++
		}
		if e.WText >= 0.25 {
			families[detector.FamilyText]++
		}
		if e.WAmp >= 0.25 {
			families[detector.FamilyAmp]++
		}
		if e.WMeta >= 0.25 {
			families[detector.FamilyMeta]++
		}
		if e.WStruct >= 0.25 {
			families[detector.FamilyStruct]++
		}
	}

	keys := make([]string, 0, len(families))
	for k := range families {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s on %d", strings.ToLower(detector.FamilyLabel(k)), families[k]))
	}

	return fmt.Sprintf(
		"%s shares %d retained behavioural edges with other accounts in this cluster (%s). "+
			"Every edge required at least two independent signal families to agree before it was retained.",
		handle, len(edges), strings.Join(parts, "; "))
}

// AccountsCSV streams the US57 export of the account annex.
//
// The header carries network id, detection run id and export timestamp
// alongside the displayed columns, because a CSV that leaves the platform has
// to say which detection it came from — a spreadsheet with no provenance is not
// evidence of anything.
func (s *NetworkService) AccountsCSV(ctx context.Context, id uuid.UUID, w io.Writer) (string, error) {
	row, err := s.visibleNetwork(ctx, id)
	if err != nil {
		return "", err
	}

	accounts, _, err := s.networks.ListNetworkAccounts(ctx, id, models.MembershipMember, repository.AccountSortCentrality, "", 0, 0)
	if err != nil {
		return "", translatePipelineErr(err, "could not load network accounts")
	}

	exportedAt := time.Now().UTC()
	cw := csv.NewWriter(w)
	header := []string{
		"network_id", "detection_run_id", "exported_at",
		"handle", "platform", "platform_account_id", "account_created_at",
		"posts_in_cluster", "duplication_rate", "median_interpost_interval_seconds",
		"circadian_coverage", "degree_centrality", "eigenvector_centrality",
		"score_contribution", "allowlisted",
	}
	if err := cw.Write(header); err != nil {
		return "", apperr.Internal("could not write the CSV header").Wrap(err)
	}

	for _, a := range accounts {
		created := ""
		if a.CreatedAtPlatform != nil {
			created = a.CreatedAtPlatform.UTC().Format(time.RFC3339)
		}
		interpost := ""
		if a.MedianInterpostSecs != nil {
			interpost = strconv.FormatFloat(*a.MedianInterpostSecs, 'f', 2, 64)
		}
		record := []string{
			row.ID.String(),
			row.RunID.String(),
			exportedAt.Format(time.RFC3339),
			a.Handle,
			a.Platform,
			a.PlatformAccountID,
			created,
			strconv.Itoa(a.PostsInCluster),
			strconv.FormatFloat(a.DuplicationRate, 'f', 4, 64),
			interpost,
			strconv.FormatFloat(a.CircadianCoverage, 'f', 4, 64),
			strconv.FormatFloat(a.DegreeCentrality, 'f', 6, 64),
			strconv.FormatFloat(a.EigenvectorCentrality, 'f', 6, 64),
			string(a.ScoreContribution),
			strconv.FormatBool(a.Allowlisted),
		}
		if err := cw.Write(record); err != nil {
			return "", apperr.Internal("could not write a CSV row").Wrap(err)
		}
	}

	cw.Flush()
	if err := cw.Error(); err != nil {
		return "", apperr.Internal("could not finish the CSV export").Wrap(err)
	}

	filename := fmt.Sprintf("CIS_NetworkAccounts_%s_%s.csv", row.ID.String(), exportedAt.Format("20060102-1504"))
	return filename, nil
}

// visibleNetwork loads a network and refuses the suppressed ones.
func (s *NetworkService) visibleNetwork(ctx context.Context, id uuid.UUID) (*repository.NetworkRow, error) {
	row, err := s.networks.FindNetworkByID(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, apperr.NotFound("coordinated network not found")
		}
		return nil, translatePipelineErr(err, "could not load coordinated network")
	}
	if row.AllowlistSuppressed {
		return nil, apperr.Forbidden(
			"this network is suppressed: at least %.0f%% of its members are on the declared-coordination allowlist",
			models.AllowlistSuppressionShare*100)
	}
	return row, nil
}

func (s *NetworkService) assertVisible(ctx context.Context, id uuid.UUID) error {
	_, err := s.visibleNetwork(ctx, id)
	return err
}

// translatePipelineErr converts a repository error into the right HTTP shape.
//
// A missing detector table is a 503 with an explanation, not a 500: the F5
// tables are provisioned by the AI service, so on a deployment where the
// pipeline has not shipped, "not available yet" is the accurate answer and a
// stack trace is not.
func translatePipelineErr(err error, message string) error {
	if errors.Is(err, repository.ErrPipelineUnavailable) {
		return apperr.Unavailable(
			"the Coordinated-Network Detector is not available: its detection tables have not been " +
				"provisioned by the AI service yet").Wrap(err)
	}
	return apperr.Internal("%s", message).Wrap(err)
}

func decodeJSONMap(raw models.JSONB) map[string]any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]any{}
	}
	return out
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
