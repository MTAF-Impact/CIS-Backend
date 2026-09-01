package service

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/cis/cis-backend/internal/dto"
	"github.com/cis/cis-backend/internal/models"
	"github.com/cis/cis-backend/internal/pkg/apperr"
	"github.com/cis/cis-backend/internal/repository"
	"github.com/cis/cis-backend/internal/scoring"
)

// AlertService serves F3, the Alert page.
type AlertService struct {
	alerts    *repository.AlertRepository
	claims    *repository.ClaimRepository
	snapshots *repository.SnapshotRepository
	settings  *SettingService
}

// NewAlertService constructs an AlertService.
func NewAlertService(
	alerts *repository.AlertRepository,
	claims *repository.ClaimRepository,
	snapshots *repository.SnapshotRepository,
	settings *SettingService,
) *AlertService {
	return &AlertService{alerts: alerts, claims: claims, snapshots: snapshots, settings: settings}
}

// List returns the [C3] watchlist table, newest addition first (US30).
//
// reader identifies whose acknowledgment decides which rows still carry the
// US71 "just crossed" highlight. It is read before the acknowledgment is
// written by the caller, so the page the user opens is the one that shows the
// highlights they are being told about.
func (s *AlertService) List(
	ctx context.Context, reader *uuid.UUID, search string, page, limit int,
) ([]dto.AlertRow, int64, dto.PageParams, error) {
	window := dto.NormalizePage(page, limit)

	threshold, err := s.settings.AlertThreshold(ctx)
	if err != nil {
		return nil, 0, window, err
	}

	acknowledged, err := s.acknowledgedAt(ctx, reader)
	if err != nil {
		return nil, 0, window, err
	}

	rows, total, err := s.alerts.ListAlerts(ctx, search, window.Limit, window.Offset())
	if err != nil {
		return nil, 0, window, apperr.Internal("could not load the watchlist").Wrap(err)
	}

	out := make([]dto.AlertRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, toAlertRow(row, threshold, acknowledged))
	}
	return out, total, window, nil
}

// Notifications returns the US71 sidebar badge: how many watched claims have
// crossed the threshold since this user last opened F3, and which ones.
func (s *AlertService) Notifications(ctx context.Context, reader *uuid.UUID) (*dto.AlertNotifications, error) {
	threshold, err := s.settings.AlertThreshold(ctx)
	if err != nil {
		return nil, err
	}

	acknowledged, err := s.acknowledgedAt(ctx, reader)
	if err != nil {
		return nil, err
	}

	count, err := s.alerts.CountCrossingsSince(ctx, acknowledged)
	if err != nil {
		return nil, apperr.Internal("could not count threshold crossings").Wrap(err)
	}

	rows, err := s.alerts.ListCrossingsSince(ctx, acknowledged, crossingListLimit)
	if err != nil {
		return nil, apperr.Internal("could not load threshold crossings").Wrap(err)
	}

	out := &dto.AlertNotifications{
		UnacknowledgedCount: count,
		AcknowledgedAt:      acknowledged,
		Threshold:           threshold,
		Crossings:           make([]dto.AlertRow, 0, len(rows)),
	}
	for _, row := range rows {
		out.Crossings = append(out.Crossings, toAlertRow(row, threshold, acknowledged))
	}
	return out, nil
}

// Acknowledge clears this user's crossing badge and row highlights (US71).
//
// US71 treats opening F3 as the acknowledgment, so the frontend calls this on
// entering the page, after rendering the list it was handed. The flagged
// alternative in the PRD — a per-row dismiss — would be a different endpoint
// shape, not a different mechanism.
func (s *AlertService) Acknowledge(ctx context.Context, reader *uuid.UUID) (*dto.AlertNotifications, error) {
	if reader == nil {
		return nil, apperr.Unauthorized("authentication required")
	}
	if err := s.alerts.Acknowledge(ctx, *reader, time.Now().UTC()); err != nil {
		return nil, apperr.Internal("could not acknowledge threshold crossings").Wrap(err)
	}
	return s.Notifications(ctx, reader)
}

// EvaluateCrossings re-derives every watched claim's Over/Under status and
// stamps the ones that just flipped (US71). Called after each score refresh.
func (s *AlertService) EvaluateCrossings(ctx context.Context) (int, error) {
	threshold, err := s.settings.AlertThreshold(ctx)
	if err != nil {
		return 0, err
	}
	return evaluateThresholdCrossings(ctx, s.alerts, threshold)
}

// acknowledgedAt reads a user's last F3 visit. An unauthenticated caller
// cannot have acknowledged anything, so every crossing counts as new.
func (s *AlertService) acknowledgedAt(ctx context.Context, reader *uuid.UUID) (*time.Time, error) {
	if reader == nil {
		return nil, nil
	}
	at, err := s.alerts.AcknowledgedAt(ctx, *reader)
	if err != nil {
		return nil, apperr.Internal("could not load alert acknowledgment").Wrap(err)
	}
	return at, nil
}

// Add appends a claim to the watchlist after the user confirms the bell dialog
// (US14).
//
// Only Existing/Generic claims may be watched: US26 bars Synthetic claims,
// since the user should not be asked to monitor predictions that may never
// materialize.
func (s *AlertService) Add(ctx context.Context, claimID uuid.UUID, addedBy *uuid.UUID) (*dto.AlertMutationResponse, error) {
	exists, rawType, err := s.claims.ClaimExists(ctx, claimID)
	if err != nil {
		return nil, apperr.Internal("could not verify claim").Wrap(err)
	}
	if !exists {
		return nil, apperr.NotFound("claim not found")
	}
	if models.NormalizeClaimType(rawType) != models.ClaimTypeExisting {
		return nil, apperr.Unprocessable(
			"only Existing (Generic) claims can be added to the Alert page; " +
				"Non-Existing (Synthetic) claims are predictions and cannot be watched")
	}

	if existing, err := s.alerts.FindByClaimID(ctx, claimID); err == nil {
		// Adding twice is not an error: the bell simply stays filled.
		return &dto.AlertMutationResponse{
			ClaimID:      claimID.String(),
			OnWatchlist:  true,
			ChartVisible: existing.ChartVisible,
			AddedAt:      &existing.AddedAt,
		}, nil
	} else if !errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.Internal("could not check the watchlist").Wrap(err)
	}

	now := time.Now().UTC()
	alert := &models.CISClaimAlert{
		ClaimID: claimID,
		AddedBy: addedBy,
		AddedAt: now,
		// Chart visibility starts off: US28 requires the chart to be empty
		// until the user explicitly checks a claim.
		ChartVisible: false,
	}
	if err := s.alerts.Create(ctx, alert); err != nil {
		return nil, apperr.Internal("could not add the claim to the watchlist").Wrap(err)
	}

	return &dto.AlertMutationResponse{
		ClaimID:      claimID.String(),
		OnWatchlist:  true,
		ChartVisible: false,
		AddedAt:      &alert.AddedAt,
	}, nil
}

// Remove deletes a claim from the watchlist (US14).
//
// Deleting the row also drops its chart_visible flag, which satisfies US14's
// requirement that removing a claim unchecks it from the chart and key.
func (s *AlertService) Remove(ctx context.Context, claimID uuid.UUID) (*dto.AlertMutationResponse, error) {
	affected, err := s.alerts.DeleteByClaimID(ctx, claimID)
	if err != nil {
		return nil, apperr.Internal("could not remove the claim from the watchlist").Wrap(err)
	}
	if affected == 0 {
		return nil, apperr.NotFound("this claim is not on the watchlist")
	}
	return &dto.AlertMutationResponse{
		ClaimID:      claimID.String(),
		OnWatchlist:  false,
		ChartVisible: false,
	}, nil
}

// SetChartVisible toggles a watched claim's [C3] chart checkbox (US28).
func (s *AlertService) SetChartVisible(ctx context.Context, claimID uuid.UUID, visible bool) (*dto.AlertMutationResponse, error) {
	affected, err := s.alerts.SetChartVisible(ctx, claimID, visible)
	if err != nil {
		return nil, apperr.Internal("could not update chart visibility").Wrap(err)
	}
	if affected == 0 {
		return nil, apperr.NotFound("this claim is not on the watchlist")
	}
	return &dto.AlertMutationResponse{
		ClaimID:      claimID.String(),
		OnWatchlist:  true,
		ChartVisible: visible,
	}, nil
}

// Chart builds the [C1] line chart and [C2] key (US27, US28).
func (s *AlertService) Chart(ctx context.Context, granularity string, from, to *time.Time) (*dto.ChartResponse, error) {
	trunc, err := repository.GranularityToTrunc(granularity)
	if err != nil {
		return nil, apperr.BadRequest("granularity must be one of: day, week, month, year")
	}

	threshold, err := s.settings.AlertThreshold(ctx)
	if err != nil {
		return nil, err
	}

	res := &dto.ChartResponse{
		Granularity: trunc,
		Threshold:   threshold,
		// US27 fixes the Y axis to the FinalClaimScore scale.
		YAxisMin: scoring.MinScore,
		YAxisMax: scoring.MaxScore,
		Series:   []dto.ChartSeries{},
	}

	claimIDs, err := s.alerts.ListChartClaimIDs(ctx)
	if err != nil {
		return nil, apperr.Internal("could not resolve charted claims").Wrap(err)
	}
	// No claims checked is the documented default empty state, not an error.
	if len(claimIDs) == 0 {
		return res, nil
	}

	points, err := s.snapshots.Series(ctx, repository.SeriesFilter{
		ClaimIDs: claimIDs,
		Trunc:    trunc,
		From:     from,
		To:       to,
	})
	if err != nil {
		return nil, apperr.Internal("could not load score history").Wrap(err)
	}

	byClaim := make(map[uuid.UUID][]dto.ScorePoint, len(claimIDs))
	for _, p := range points {
		byClaim[p.ClaimID] = append(byClaim[p.ClaimID], dto.ScorePoint{
			BucketStart:     p.BucketStart,
			FinalClaimScore: scoring.ClampPtr(p.FinalClaimScore),
			ClaimScore:      scoring.ClampPtr(p.ClaimScore),
			SampleCount:     p.SampleCount,
		})
	}

	for _, claimID := range claimIDs {
		row, err := s.claims.FindClaimByID(ctx, claimID)
		if err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				continue
			}
			return nil, apperr.Internal("could not load charted claim").Wrap(err)
		}

		series := dto.ChartSeries{
			ClaimID:        claimID.String(),
			ClaimStatement: row.ClaimStatement,
			Points:         byClaim[claimID],
		}
		if series.Points == nil {
			series.Points = []dto.ScorePoint{}
		}
		if row.TopicName != nil {
			series.Topic = &dto.TopicRef{ID: row.TopicID.String(), Name: *row.TopicName}
		}
		res.Series = append(res.Series, series)
	}
	return res, nil
}

// CaptureSnapshots records the current scores of every watched claim, building
// the history the chart plots. Invoked by the cron job and exposed for manual
// triggering.
//
// Only watched claims are captured: they are the only ones F3 charts, and
// snapshotting the whole claim table every hour would grow without bound.
func (s *AlertService) CaptureSnapshots(ctx context.Context) (int, error) {
	rows, _, err := s.alerts.ListAlerts(ctx, "", 1000, 0)
	if err != nil {
		return 0, apperr.Internal("could not load the watchlist").Wrap(err)
	}
	if len(rows) == 0 {
		return 0, nil
	}

	claimIDs := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		claimIDs = append(claimIDs, row.ClaimID)
	}

	count, err := s.snapshots.CaptureForClaims(ctx, claimIDs, time.Now().UTC())
	if err != nil {
		return 0, apperr.Internal("could not capture score snapshots").Wrap(err)
	}
	return count, nil
}

// crossingListLimit caps how many crossings the notification payload names.
// The badge is a count; the list beside it is a preview, and a watchlist where
// dozens crossed at once is a threshold problem, not a paging problem.
const crossingListLimit = 20

// PruneSnapshots deletes snapshot history older than the retention window.
func (s *AlertService) PruneSnapshots(ctx context.Context, retention time.Duration) (int64, error) {
	deleted, err := s.snapshots.DeleteOlderThan(ctx, time.Now().UTC().Add(-retention))
	if err != nil {
		return 0, apperr.Internal("could not prune score snapshots").Wrap(err)
	}
	return deleted, nil
}

func toAlertRow(row repository.AlertRow, threshold float64, acknowledgedAt *time.Time) dto.AlertRow {
	out := dto.AlertRow{
		ID:              row.ClaimID.String(),
		AlertID:         row.AlertID.String(),
		ClaimStatement:  row.ClaimStatement,
		ClaimCreatedAt:  row.ClaimCreatedAt,
		AddedAt:         row.AddedAt,
		ChartVisible:    row.ChartVisible,
		ReviewStatus:    row.ReviewStatus,
		FinalClaimScore: scoring.ClampPtr(row.FinalClaimScore),
		Threshold:       threshold,
		IsDormant:       row.IsDormant,
		ThresholdStatus: dto.ThresholdUnder,
	}
	if row.TopicName != nil && row.TopicID != nil {
		out.Topic = &dto.TopicRef{ID: row.TopicID.String(), Name: *row.TopicName}
	}
	// US29: Over/Under is decided by comparing FinalClaimScore against the F4
	// global threshold. An unscored claim stays Under rather than being
	// escalated on missing data.
	if out.FinalClaimScore != nil && *out.FinalClaimScore >= threshold {
		out.ThresholdStatus = dto.ThresholdOver
	}

	// US29/US71: the row highlight marks a status that *just* flipped, which is
	// a fact about the reader as much as about the claim — it lasts until they
	// have seen it. The crossing itself stays on the row either way.
	out.CrossedAt = row.CrossedAt
	out.CrossedDirection = row.CrossedDirection
	out.JustCrossed = row.CrossedAt != nil &&
		(acknowledgedAt == nil || row.CrossedAt.After(*acknowledgedAt))
	return out
}
