package service

import (
	"context"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/cis/cis-backend/internal/dto"
	"github.com/cis/cis-backend/internal/models"
	"github.com/cis/cis-backend/internal/pkg/apperr"
	"github.com/cis/cis-backend/internal/repository"
	"github.com/cis/cis-backend/internal/scoring"
)

// OverviewService serves the Overview page.
//
// It computes rather than reads: the Overview page's numbers are aggregates
// over the same claim rows the claim list ranks and the same content stream
// the AI service scores. None of them is stored, because a stored copy is a
// number that can disagree with the page it summarises.
type OverviewService struct {
	overview *repository.OverviewRepository
	policies *repository.PolicyRepository
	settings *SettingService
}

// NewOverviewService constructs an OverviewService.
func NewOverviewService(
	overview *repository.OverviewRepository,
	policies *repository.PolicyRepository,
	settings *SettingService,
) *OverviewService {
	return &OverviewService{overview: overview, policies: policies, settings: settings}
}

// Page builds the whole Overview payload: the sentiment gauge, the topic
// treemap, and the policy leaderboard.
func (s *OverviewService) Page(ctx context.Context, policyLimit int) (*dto.OverviewResponse, error) {
	threshold, err := s.settings.AlertThreshold(ctx)
	if err != nil {
		return nil, err
	}
	city := s.settings.MonitoredCity(ctx)
	ranking := s.settings.OverviewRanking(ctx)
	now := time.Now().UTC()

	res := &dto.OverviewResponse{
		City: dto.CityScope{
			Name:        city.Name,
			Province:    city.Province,
			Timezone:    city.Timezone,
			Partitioned: s.overview.HasCity(),
		},
		GeneratedAt: now,
	}

	counts, err := s.overview.ThresholdCounts(ctx, city.Name, threshold)
	if err != nil {
		return nil, apperr.Internal("could not count claims against the threshold").Wrap(err)
	}
	res.ThresholdRatio = dto.ThresholdRatio{
		Above:        counts.Above,
		Below:        counts.Below,
		Total:        counts.Total,
		AbovePercent: percent(counts.Above, counts.Total),
		Threshold:    threshold,
	}

	sentiment, err := s.sentimentIndex(ctx, city.Name, now)
	if err != nil {
		return nil, err
	}
	res.Sentiment = *sentiment

	topics, err := s.overview.TopicAggregates(ctx, city.Name, threshold)
	if err != nil {
		return nil, apperr.Internal("could not aggregate claims by topic").Wrap(err)
	}
	res.Topics = buildTopicBoxes(topics, ranking)

	if policyLimit <= 0 {
		policyLimit = ranking.TopPolicyLimit
	}
	res.Policies, err = s.hotPolicies(ctx, city.Name, threshold, policyLimit, ranking)
	if err != nil {
		return nil, err
	}
	return res, nil
}

// Topic builds the treemap's click-through modal for one topic.
func (s *OverviewService) Topic(ctx context.Context, topicID uuid.UUID) (*dto.TopicOverviewDetail, error) {
	threshold, err := s.settings.AlertThreshold(ctx)
	if err != nil {
		return nil, err
	}
	city := s.settings.MonitoredCity(ctx)

	agg, err := s.overview.TopicAggregate(ctx, city.Name, topicID, threshold)
	if err != nil {
		if err == repository.ErrNotFound {
			return nil, apperr.NotFound("topic not found, or it has no Existing claims in this city")
		}
		return nil, apperr.Internal("could not aggregate the topic").Wrap(err)
	}

	below := agg.ClaimCount - agg.AboveCount
	out := &dto.TopicOverviewDetail{
		Topic:               dto.TopicRef{ID: agg.TopicID.String(), Name: agg.TopicName},
		ClaimCount:          agg.ClaimCount,
		AboveThresholdCount: agg.AboveCount,
		BelowThresholdCount: below,
		AverageScore:        scoring.ClampPtr(agg.AvgScore),
		Threshold:           threshold,
	}
	// Undefined rather than infinite when everything is above threshold. A UI
	// that prints "Infinity" beside a policy risk figure is worse than one that
	// prints nothing.
	if below > 0 {
		ratio := float64(agg.AboveCount) / float64(below)
		out.AboveUnderRatio = &ratio
	}

	now := time.Now().UTC()
	month := time.Duration(s.settings.OverviewRanking(ctx).MoMWindowDays) * 24 * time.Hour
	current, err := s.overview.TopicScoreAverage(ctx, topicID, now.Add(-month), now)
	if err != nil {
		return nil, apperr.Internal("could not read this month's score history").Wrap(err)
	}
	previous, err := s.overview.TopicScoreAverage(ctx, topicID, now.Add(-2*month), now.Add(-month))
	if err != nil {
		return nil, apperr.Internal("could not read last month's score history").Wrap(err)
	}

	out.CurrentMonthAverage = scoring.ClampPtr(current)
	out.PreviousMonthAverage = scoring.ClampPtr(previous)
	// A percentage change needs both sides, and a previous average of zero
	// makes it undefined rather than infinite.
	if current != nil && previous != nil && *previous > 0 {
		change := (*current - *previous) / *previous * 100
		direction := directionOf(change)
		out.AverageScoreMoMPercent = &change
		out.MoMDirection = &direction
	}
	return out, nil
}

// sentimentIndex computes the gauge's Climate Sentiment Index over the 7-day
// rolling window, with the 24h-lagged momentum indicator.
func (s *OverviewService) sentimentIndex(ctx context.Context, city string, now time.Time) (*dto.SentimentIndex, error) {
	params, err := s.settings.CSIParams(ctx)
	if err != nil {
		return nil, err
	}

	window := time.Duration(params.WindowDays) * 24 * time.Hour
	from := now.Add(-window)

	out := &dto.SentimentIndex{
		Status:        dto.CSIStatusUnavailable,
		WindowStart:   from,
		WindowEnd:     now,
		WindowDays:    params.WindowDays,
		MinimumVolume: params.MinimumVolume,
		RiskThreshold: params.RiskThreshold,
		WeightBCS:     params.WeightBCS,
		WeightRisk:    params.WeightRiskLoad,
	}

	if !s.overview.HasSentiment() {
		out.Reason = "The AI service has not provisioned per-item climate sentiment yet, " +
			"so Baseline Climate Sentiment cannot be computed. See docs/sql/02_f6_reference_schema.sql."
		return out, nil
	}

	current, err := s.computeCSI(ctx, city, from, now, params)
	if err != nil {
		return nil, err
	}

	out.Volume = dto.ConversationVolume{
		Total:    current.volumes.Total,
		Positive: current.volumes.Positive,
		Negative: current.volumes.Negative,
		Neutral:  current.volumes.Neutral,
	}

	// Below the minimum activity threshold the index would report a falsely
	// calm environment from low engagement, so it reports nothing.
	if current.volumes.Total < params.MinimumVolume {
		out.Status = dto.CSIStatusInsufficientData
		out.Reason = "Not enough climate conversation in the window to compute a reliable index."
		return out, nil
	}

	out.Status = dto.CSIStatusOK
	out.Score = &current.csi
	band := params.Band(current.csi)
	out.Band = &band
	out.BCS = &current.bcs
	out.BCSNormalized = &current.bcsNormalized
	out.RiskLoad = &current.riskLoad

	// Momentum: the same index over a window lagged by 24h. A lagged window
	// that is itself too thin yields no direction rather than a direction
	// computed from noise.
	lag := time.Duration(params.MomentumLagHours) * time.Hour
	previous, err := s.computeCSI(ctx, city, from.Add(-lag), now.Add(-lag), params)
	if err != nil {
		return nil, err
	}
	if previous.volumes.Total >= params.MinimumVolume {
		delta := current.csi - previous.csi
		direction := directionOf(delta)
		out.Momentum = &delta
		out.MomentumDirection = &direction
	}
	return out, nil
}

// csiWindow is one evaluation of the index over a time window.
type csiWindow struct {
	volumes       repository.ConversationVolumes
	bcs           float64
	bcsNormalized float64
	riskLoad      float64
	csi           float64
}

func (s *OverviewService) computeCSI(
	ctx context.Context, city string, from, to time.Time, params scoring.CSIParams,
) (csiWindow, error) {
	var out csiWindow

	volumes, err := s.overview.ConversationVolumes(ctx, city, from, to)
	if err != nil {
		return out, apperr.Internal("could not measure climate conversation volume").Wrap(err)
	}
	out.volumes = volumes
	if volumes.Total <= 0 {
		return out, nil
	}

	weighted, err := s.overview.WeightedRiskScore(ctx, city, from, to, params.RiskThreshold)
	if err != nil {
		return out, apperr.Internal("could not measure claim risk load").Wrap(err)
	}

	out.bcs = scoring.BCS(volumes.Positive, volumes.Negative, volumes.Total)
	out.bcsNormalized = scoring.BCSNormalized(out.bcs)
	out.riskLoad = scoring.RiskLoad(weighted, volumes.Total)
	out.csi = params.Index(out.bcsNormalized, out.riskLoad)
	return out, nil
}

// hotPolicies ranks policies by the combined metric and resolves their
// display names.
func (s *OverviewService) hotPolicies(
	ctx context.Context, city string, threshold float64, limit int, ranking OverviewRanking,
) ([]dto.HotPolicy, error) {
	aggregates, err := s.overview.PolicyAggregates(ctx, city, threshold)
	if err != nil {
		return nil, apperr.Internal("could not aggregate claims by policy").Wrap(err)
	}

	counts := make([]int64, 0, len(aggregates))
	scores := make([]*float64, 0, len(aggregates))
	for _, a := range aggregates {
		counts = append(counts, a.AboveCount)
		scores = append(scores, a.AvgScore)
	}
	sizes := combinedMetric(counts, scores, ranking)

	rows := make([]dto.HotPolicy, 0, len(aggregates))
	ids := make([]uuid.UUID, 0, len(aggregates))
	for i, a := range aggregates {
		rows = append(rows, dto.HotPolicy{
			ClaimCount:          a.ClaimCount,
			AboveThresholdCount: a.AboveCount,
			AverageScore:        scoring.ClampPtr(a.AvgScore),
			Score:               sizes[i],
			Policy:              dto.PolicyRef{ID: a.AIPolicyID.String()},
		})
		ids = append(ids, a.AIPolicyID)
	}

	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Score > rows[j].Score })
	if len(rows) > limit {
		rows = rows[:limit]
	}

	names, err := s.policyNames(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		id, _ := uuid.Parse(rows[i].Policy.ID)
		if ref, ok := names[id]; ok {
			rows[i].Policy = ref
		}
		rows[i].Rank = i + 1
	}
	return rows, nil
}

// policyNames resolves AI policy ids onto display references, preferring this
// backend's own policy record where one shadows the AI policy — the same
// precedence the claim detail page uses, so a policy is never named two
// different things in two places.
func (s *OverviewService) policyNames(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]dto.PolicyRef, error) {
	out := make(map[uuid.UUID]dto.PolicyRef, len(ids))
	if len(ids) == 0 {
		return out, nil
	}

	aiPolicies, err := s.policies.FindAIPoliciesByIDs(ctx, ids)
	if err != nil {
		return nil, apperr.Internal("could not load AI policy records").Wrap(err)
	}
	for _, p := range aiPolicies {
		aiID := p.ID.String()
		out[p.ID] = dto.PolicyRef{ID: aiID, Name: p.Title, Source: "ai", AIPolicyID: &aiID}
	}

	cisPolicies, err := s.policies.FindByAIPolicyIDs(ctx, ids)
	if err != nil {
		return nil, apperr.Internal("could not load policy records").Wrap(err)
	}
	for _, p := range cisPolicies {
		if p.AIPolicyID == nil {
			continue
		}
		aiID := p.AIPolicyID.String()
		status := p.Status
		rolledOut := p.RolledOutDate
		out[*p.AIPolicyID] = dto.PolicyRef{
			ID:            p.ID.String(),
			Name:          p.Name,
			Source:        "cis",
			AIPolicyID:    &aiID,
			Status:        &status,
			RolledOutDate: &rolledOut,
			HasDocument:   p.FilePath != "",
		}
	}
	return out, nil
}

// buildTopicBoxes turns per-topic aggregates into sized treemap rectangles,
// largest first.
func buildTopicBoxes(aggregates []repository.TopicAggregate, ranking OverviewRanking) []dto.TopicBox {
	counts := make([]int64, 0, len(aggregates))
	scores := make([]*float64, 0, len(aggregates))
	for _, a := range aggregates {
		counts = append(counts, a.AboveCount)
		scores = append(scores, a.AvgScore)
	}
	sizes := combinedMetric(counts, scores, ranking)

	boxes := make([]dto.TopicBox, 0, len(aggregates))
	for i, a := range aggregates {
		boxes = append(boxes, dto.TopicBox{
			Topic:               dto.TopicRef{ID: a.TopicID.String(), Name: a.TopicName},
			ClaimCount:          a.ClaimCount,
			AboveThresholdCount: a.AboveCount,
			AverageScore:        scoring.ClampPtr(a.AvgScore),
			BoxSize:             sizes[i],
		})
	}
	sort.SliceStable(boxes, func(i, j int) bool { return boxes[i].BoxSize > boxes[j].BoxSize })
	return boxes
}

// combinedMetric implements the shared treemap box-size and policy ranking
// formula: normalise each input against the largest value in the current set,
// then weight the two by their configured shares. The default is an equal
// split, mirroring the CSI's own 50/50 weighting, and deliberately leaves the
// weighting open — these two settings are that open question made adjustable.
//
// Normalising against the set maximum rather than a fixed ceiling is what makes
// the treemap readable: the count of above-threshold claims has no natural
// upper bound, and dividing by one would flatten every rectangle in a quiet
// week into the same size.
//
// A set where every count is zero contributes zero from that half rather than
// dividing by zero, leaving the average score to order the topics alone.
func combinedMetric(counts []int64, scores []*float64, ranking OverviewRanking) []float64 {
	var maxCount int64
	var maxScore float64
	for i := range counts {
		if counts[i] > maxCount {
			maxCount = counts[i]
		}
		if scores[i] != nil && *scores[i] > maxScore {
			maxScore = *scores[i]
		}
	}

	out := make([]float64, len(counts))
	for i := range counts {
		var normalizedCount, normalizedScore float64
		if maxCount > 0 {
			normalizedCount = float64(counts[i]) / float64(maxCount) * scoring.MaxScore
		}
		if maxScore > 0 && scores[i] != nil {
			normalizedScore = *scores[i] / maxScore * scoring.MaxScore
		}
		out[i] = ranking.WeightAboveCount*normalizedCount + ranking.WeightAvgScore*normalizedScore
	}
	return out
}

// percent is a share of a total, guarding the empty-repository case.
func percent(part, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(part) / float64(total) * 100
}

// directionOf labels a delta for the UI's up/down/flat arrow.
func directionOf(delta float64) string {
	switch {
	case delta > 0:
		return models.CrossingDirectionUp
	case delta < 0:
		return models.CrossingDirectionDown
	default:
		return "flat"
	}
}
