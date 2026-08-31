package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"

	"github.com/cis/cis-backend/internal/aiclient"
	"github.com/cis/cis-backend/internal/detector"
	"github.com/cis/cis-backend/internal/dto"
	"github.com/cis/cis-backend/internal/models"
	"github.com/cis/cis-backend/internal/pkg/apperr"
	"github.com/cis/cis-backend/internal/repository"
)

// DetectionService owns the three ways a detection run starts (PRD 10.5.8) and
// the two read-only recalibration surfaces the PRD attaches to them.
//
// It does not run the detection. The maths — Leiden community detection,
// MinHash with LSH banding, multilingual sentence embeddings, perceptual image
// hashing, a Poisson-binomial null model, ForceAtlas2 layout — is mature in
// Python and effectively absent in Go, and the same split already governs the
// Section 6 claim scores: the AI service computes, this backend reads and
// presents. What lives here is the scheduling, the scope rules, and the
// governance around them.
type DetectionService struct {
	networks  *repository.NetworkRepository
	claims    *repository.ClaimRepository
	settings  *SettingService
	allowlist *AllowlistService
	ai        *aiclient.Client
}

// NewDetectionService constructs a DetectionService.
func NewDetectionService(
	networks *repository.NetworkRepository,
	claims *repository.ClaimRepository,
	settings *SettingService,
	allowlist *AllowlistService,
	ai *aiclient.Client,
) *DetectionService {
	return &DetectionService{networks: networks, claims: claims, settings: settings, allowlist: allowlist, ai: ai}
}

// MaxScheduledClaims caps how many claims one scheduled sweep hands over.
//
// PRD 10.5.8 schedules runs "across all claims with status Active" and sets a
// 10-minute budget for a single 5,000-account run. A city with hundreds of
// active claims would otherwise queue more work every six hours than six hours
// can absorb, and a detector that never finishes a cycle silently stops being a
// detector. Claims are handed over highest-score first, so the cap sheds the
// least consequential work.
const MaxScheduledClaims = 200

// Trigger starts an on-demand detection run (PRD 10.5.8 item 3).
func (s *DetectionService) Trigger(
	ctx context.Context, claimIDs []uuid.UUID, source string,
) (*dto.TriggerDetectionResponse, error) {
	if len(claimIDs) == 0 {
		return nil, apperr.BadRequest("at least one claim id is required")
	}

	// PRD 10.3 puts detection over Non-Existing/Synthetic claims (S2) out of
	// scope: predicted claims have no real posts, so there is nothing to
	// cluster. Rejecting here rather than letting the pipeline discover it
	// keeps the error where the user can act on it.
	for _, id := range claimIDs {
		exists, rawType, err := s.claims.ClaimExists(ctx, id)
		if err != nil {
			return nil, apperr.Internal("could not verify the claim").Wrap(err)
		}
		if !exists {
			return nil, apperr.NotFound("claim %s not found", id)
		}
		if models.NormalizeClaimType(rawType) != models.ClaimTypeExisting {
			return nil, apperr.Unprocessable(
				"claim %s is a Non-Existing/Synthetic claim. Detection runs only over Existing/Generic claims: "+
					"a predicted claim has no real posts, so there is nothing to cluster (PRD 10.3)", id)
		}
	}

	return s.dispatch(ctx, claimIDs, source)
}

// dispatch computes the window, gathers the exclusions, and hands the run to
// the AI service.
func (s *DetectionService) dispatch(
	ctx context.Context, claimIDs []uuid.UUID, source string,
) (*dto.TriggerDetectionResponse, error) {
	settings, err := s.settings.DetectorSettings(ctx)
	if err != nil {
		return nil, err
	}

	res := &dto.TriggerDetectionResponse{
		ClaimIDs: uuidStrings(claimIDs),
		Status:   models.RunStatusPending,
	}

	if !s.ai.Enabled() {
		// Degrading gracefully rather than erroring matches how every other AI
		// hand-off behaves when AI_SERVICE_URL is unset, so the backend stays
		// demoable without the detector.
		res.Status = "unavailable"
		res.Message = "AI_SERVICE_URL is not configured, so no detection run was started."
		return res, apperr.Unavailable(
			"the detection pipeline is not configured: set AI_SERVICE_URL to enable coordinated-network detection")
	}

	// The window is computed here rather than by the AI service so that
	// PRD 10.5.1's 50% overlap rule lives in the one place it can be enforced.
	// The cross-field validation in CISDetectorSettings.Validate guarantees
	// cadence <= W/2, which is what makes consecutive runs overlap; computing
	// the window on the far side of the hand-off would put the guarantee out of
	// reach of its own check.
	now := time.Now().UTC()
	windowEnd := now
	windowStart := now.Add(-settings.Window())

	exclusions, err := s.allowlist.Exclusions(ctx)
	if err != nil {
		return nil, err
	}

	// The whole parameter set travels with the request. US62: changing a
	// parameter must never retroactively alter a stored detection, so the run
	// records what was in force when it executed rather than looking it up
	// later.
	ack, err := s.ai.TriggerDetection(ctx, aiclient.DetectionRunRequest{
		ClaimIDs:      claimIDs,
		TriggerSource: source,
		WindowStart:   windowStart,
		WindowEnd:     windowEnd,
		Parameters:    toDetectorSettingsView(*settings),
		Exclusions:    exclusions,
	})
	if err != nil {
		if errors.Is(err, aiclient.ErrNotConfigured) {
			return nil, apperr.Unavailable("the detection pipeline is not configured")
		}
		return nil, apperr.Unavailable("the AI service could not start a detection run").Wrap(err)
	}

	runID := ack.RunID.String()
	res.RunID = &runID
	if ack.Status != "" {
		res.Status = ack.Status
	}
	res.Message = fmt.Sprintf(
		"Detection run started over %d claim(s), window %s to %s. Results appear as the run completes.",
		len(claimIDs), windowStart.Format(time.RFC3339), windowEnd.Format(time.RFC3339))
	return res, nil
}

// RunScheduled starts the periodic sweep over Active Existing claims
// (PRD 10.5.8 item 1). Returns how many claims were handed over, and 0 when the
// configured cadence has not yet elapsed.
//
// The cadence is a detector setting (1-24 h), which is why the caller cannot
// simply be a cron expression: cron specs are fixed when the scheduler starts
// and this one is edited in F4 at runtime. The job ticks frequently — at least
// as often as the finest cadence — and this method decides whether the tick is
// due. That also makes the setting take effect on the next tick rather than on
// the next restart, which is what an admin changing it expects.
func (s *DetectionService) RunScheduled(ctx context.Context) (int, error) {
	if !s.ai.Enabled() {
		return 0, nil
	}

	settings, err := s.settings.DetectorSettings(ctx)
	if err != nil {
		return 0, err
	}
	last, err := s.networks.LastRunStartedAt(ctx, models.RunTriggerScheduled)
	if err != nil {
		// No pipeline tables yet means no run has ever happened, which is the
		// same answer as "never run" — and dispatch below will report the
		// unavailability properly if it is a real outage.
		if !errors.Is(err, repository.ErrPipelineUnavailable) {
			return 0, fmt.Errorf("resolve last scheduled run: %w", err)
		}
		last = nil
	}
	if last != nil && time.Since(*last) < settings.Cadence() {
		return 0, nil
	}

	claimIDs, err := s.networks.ActiveClaimIDsForDetection(ctx, MaxScheduledClaims)
	if err != nil {
		return 0, fmt.Errorf("resolve active claims: %w", err)
	}
	if len(claimIDs) == 0 {
		return 0, nil
	}

	if _, err := s.dispatch(ctx, claimIDs, models.RunTriggerScheduled); err != nil {
		return 0, err
	}
	return len(claimIDs), nil
}

// RunVelocityTriggered starts a run for claims whose Velocity has spiked
// (PRD 10.5.8 item 2).
//
// A sudden growth spike is exactly when a network is most likely present and
// most detectable, which is the entire justification for an unscheduled run.
//
// It is off by default: PRD 10.11 omits this parameter altogether — no stated
// default, no stated range — so firing on a number nobody chose would mean
// either launching a ten-minute run on every ordinary news cycle or never
// firing at all, with no way to tell which. See PRD-v1.4.md open question 8.
func (s *DetectionService) RunVelocityTriggered(ctx context.Context) (int, error) {
	if !s.ai.Enabled() {
		return 0, nil
	}

	settings, err := s.settings.DetectorSettings(ctx)
	if err != nil {
		return 0, err
	}
	if !settings.VelocityTriggerEnabled {
		return 0, nil
	}

	claimIDs, err := s.networks.VelocityTriggeredClaimIDs(ctx, settings.VelocityTriggerThreshold, MaxScheduledClaims)
	if err != nil {
		return 0, fmt.Errorf("resolve velocity-triggered claims: %w", err)
	}
	if len(claimIDs) == 0 {
		return 0, nil
	}

	if _, err := s.dispatch(ctx, claimIDs, models.RunTriggerVelocity); err != nil {
		return 0, err
	}
	return len(claimIDs), nil
}

// PurgeExpiredSnapshots applies PRD 10.9.1 rule 7's retention with its
// exception.
//
// Evidence snapshots are kept for a configurable period, default 24 months, and
// then purged — EXCEPT where a report has been generated from them, in which
// case the snapshot lives as long as the report. That exception is not
// decoration: a report whose evidence has been purged is worthless as evidence,
// and a platform referral submitted last year is exactly the document whose
// evidence someone will ask to see.
//
// So this can never be a blanket TTL delete. The backend selects the eligible
// snapshots — it is the only side that can see cis_network_reports — and hands
// the list to the AI service, which owns the rows.
func (s *DetectionService) PurgeExpiredSnapshots(ctx context.Context) (int, error) {
	if !s.ai.Enabled() {
		return 0, nil
	}

	ids, err := s.networks.ExpiredSnapshotNetworkIDs(ctx, time.Now().UTC(), 500)
	if err != nil {
		if errors.Is(err, repository.ErrPipelineUnavailable) {
			return 0, nil
		}
		return 0, fmt.Errorf("resolve expired snapshots: %w", err)
	}
	if len(ids) == 0 {
		return 0, nil
	}

	res, err := s.ai.PurgeExpiredSnapshots(ctx, aiclient.PurgeSnapshotsRequest{NetworkIDs: ids})
	if err != nil {
		if errors.Is(err, aiclient.ErrNotConfigured) {
			return 0, nil
		}
		return 0, fmt.Errorf("purge snapshots: %w", err)
	}
	log.Printf("[detection] purged %d expired evidence snapshots (reports retained)", res.SnapshotsPurged)
	return res.SnapshotsPurged, nil
}

// Run returns one detection run (US62's run history).
func (s *DetectionService) Run(ctx context.Context, runID uuid.UUID) (*dto.DetectionRunView, error) {
	run, err := s.networks.FindRun(ctx, runID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, apperr.NotFound("detection run not found")
		}
		return nil, translatePipelineErr(err, "could not load the detection run")
	}
	view := toRunView(repository.RunRow{AIDetectionRun: *run})
	return &view, nil
}

// ListRuns returns the detection-run history.
//
// This exists because truncation and signal unavailability are RUN-level facts
// that cap confidence for every network in that run (PRD 10.6.3 rule 4). "Why
// is everything Medium this week?" is a question about runs, not about
// networks, and without this surface it has no answer.
func (s *DetectionService) ListRuns(
	ctx context.Context, f repository.RunFilter, page, limit int,
) ([]dto.DetectionRunView, int64, dto.PageParams, error) {
	window := dto.NormalizePage(page, limit)
	f.Limit = window.Limit
	f.Offset = window.Offset()

	rows, total, err := s.networks.ListRuns(ctx, f)
	if err != nil {
		return nil, 0, window, translatePipelineErr(err, "could not load detection runs")
	}

	out := make([]dto.DetectionRunView, 0, len(rows))
	for _, r := range rows {
		out = append(out, toRunView(r))
	}
	return out, total, window, nil
}

func toRunView(r repository.RunRow) dto.DetectionRunView {
	unavailable := []string(r.SignalsUnavailable)
	if unavailable == nil {
		unavailable = []string{}
	}

	view := dto.DetectionRunView{
		RunID:                    r.ID.String(),
		Status:                   r.Status,
		TriggerSource:            r.TriggerSource,
		ScopeClaimIDs:            []string(r.ScopeClaimIDs),
		WindowStart:              r.WindowStart,
		WindowEnd:                r.WindowEnd,
		Truncated:                r.Truncated,
		CandidatesCount:          r.CandidatesCount,
		SignalsUnavailable:       labelFamilies(unavailable),
		ConfidenceCappedAtMedium: r.CapsConfidenceAtMedium(),
		NetworkCount:             r.NetworkCount,
		OfftopicCount:            r.OfftopicCount,
		RandomSeed:               r.RandomSeed,
		StartedAt:                r.StartedAt,
		CompletedAt:              r.CompletedAt,
		Error:                    r.Error,
	}
	if view.ScopeClaimIDs == nil {
		view.ScopeClaimIDs = []string{}
	}
	if len(r.ParametersJSON) > 0 {
		view.Parameters = json.RawMessage(r.ParametersJSON)
	}
	return view
}

// OfftopicClusters returns the read-only recalibration view (US62).
//
// These clusters are real coordinated clusters — commercial spam rings,
// engagement farms, unrelated political amplification — that happened to pass
// through a climate claim and failed the relevance gate. They are never
// surfaced in the network list and never exported in a report, because they are
// not the city's problem and must not appear in a climate report. They are
// retained for one purpose only, and this endpoint is it: a rising off-topic
// rate tells an admin that omega_min or the candidate scope needs recalibration.
func (s *DetectionService) OfftopicClusters(
	ctx context.Context, f repository.OfftopicFilter, page, limit int,
) ([]dto.OfftopicClusterView, int64, dto.PageParams, error) {
	window := dto.NormalizePage(page, limit)
	f.Limit = window.Limit
	f.Offset = window.Offset()

	rows, total, err := s.networks.ListOfftopicClusters(ctx, f)
	if err != nil {
		return nil, 0, window, translatePipelineErr(err, "could not load off-topic clusters")
	}

	out := make([]dto.OfftopicClusterView, 0, len(rows))
	for _, r := range rows {
		view := dto.OfftopicClusterView{
			ClusterID:      r.ID.String(),
			RunID:          r.RunID.String(),
			ClaimID:        r.ClaimID.String(),
			ClaimStatement: r.ClaimStatement,
			FailedTest:     r.FailedTest,
			OverlapRatio:   r.OverlapRatio,
			AnchoringShare: r.AnchoringShare,
			AccountCount:   r.AccountCount,
			PostCount:      r.PostCount,
			CreatedAt:      r.CreatedAt,
		}
		if len(r.CoordinationSignals) > 0 {
			view.Signals = json.RawMessage(r.CoordinationSignals)
		}
		out = append(out, view)
	}
	return out, total, window, nil
}

// OfftopicRates returns per-run surfaced-vs-rejected ratios (US62).
func (s *DetectionService) OfftopicRates(ctx context.Context, limit int) ([]dto.OfftopicRate, error) {
	if limit <= 0 {
		limit = 30
	}

	rows, err := s.networks.OfftopicRates(ctx, limit)
	if err != nil {
		return nil, translatePipelineErr(err, "could not compute off-topic rates")
	}

	out := make([]dto.OfftopicRate, 0, len(rows))
	for _, r := range rows {
		rate := dto.OfftopicRate{
			RunID:         r.RunID.String(),
			StartedAt:     r.StartedAt,
			SurfacedCount: r.SurfacedCount,
			OfftopicCount: r.OfftopicCount,
			FailedTests:   splitNonEmpty(r.FailedTests),
		}
		if total := r.SurfacedCount + r.OfftopicCount; total > 0 {
			rate.Rate = float64(r.OfftopicCount) / float64(total)
		}
		out = append(out, rate)
	}
	return out, nil
}

// Dismissals returns recorded false-positive dismissals with their snapshotted
// signal profiles (PRD 10.9.3).
func (s *DetectionService) Dismissals(
	ctx context.Context, from, to *time.Time, page, limit int,
) ([]dto.DismissalView, int64, dto.PageParams, error) {
	window := dto.NormalizePage(page, limit)

	rows, total, err := s.networks.ListDismissals(ctx, from, to, window.Limit, window.Offset())
	if err != nil {
		return nil, 0, window, apperr.Internal("could not load dismissals").Wrap(err)
	}

	out := make([]dto.DismissalView, 0, len(rows))
	for _, r := range rows {
		view := dto.DismissalView{
			ID:           r.ID.String(),
			NetworkID:    r.NetworkID.String(),
			NetworkLabel: r.NetworkLabel,
			Reason:       r.Reason,
			CreatedAt:    r.CreatedAt,
		}
		if r.UserID != nil {
			id := r.UserID.String()
			view.UserID = &id
		}
		if len(r.SignalProfile) > 0 {
			view.SignalProfile = json.RawMessage(r.SignalProfile)
		}
		out = append(out, view)
	}
	return out, total, window, nil
}

// DismissalSummary is PRD 10.9.3's aggregate: the dismissal rate and the mean
// signal profile of dismissals, so the team can identify a systematically
// over-triggering signal and recalibrate beta_k or the thresholds in F4.
//
// It also computes the precision figure the PRD sets a target on. Precision is
// deliberately the metric and recall is deliberately secondary — "a missed
// network costs a missed referral; a false positive costs a government publicly
// implying that residents are bots."
func (s *DetectionService) DismissalSummary(ctx context.Context, windowDays int) (*dto.DismissalSummary, error) {
	if windowDays <= 0 {
		windowDays = detector.PrecisionWindowDays
	}
	since := time.Now().UTC().AddDate(0, 0, -windowDays)

	counts, err := s.networks.CountDecisions(ctx, since)
	if err != nil {
		return nil, apperr.Internal("could not count review decisions").Wrap(err)
	}

	out := &dto.DismissalSummary{
		WindowDays:      windowDays,
		Confirmed:       counts.Confirmed,
		ActionTaken:     counts.ActionTaken,
		Dismissed:       counts.Dismissed,
		PrecisionTarget: detector.PrecisionTarget,
	}

	if total := counts.Confirmed + counts.ActionTaken + counts.Dismissed; total > 0 {
		p := float64(counts.Confirmed+counts.ActionTaken) / float64(total)
		meets := p >= detector.PrecisionTarget
		out.Precision = &p
		out.MeetsTarget = &meets
	} else {
		out.Note = "No terminal review decisions were recorded in this window, so precision is undefined. " +
			"The figure becomes meaningful once analysts have confirmed or dismissed networks."
	}

	// The mean profile comes from the LOG rows, not from a join back to the
	// networks. That is the point of snapshotting the profile at the moment of
	// dismissal: a later detection run can recompute a network's scores, and an
	// average taken over drifting numbers cannot answer which signal is
	// over-triggering.
	dismissals, _, err := s.networks.ListDismissals(ctx, &since, nil, 1000, 0)
	if err != nil {
		return nil, apperr.Internal("could not load dismissal profiles").Wrap(err)
	}

	sums := map[string]float64{}
	counted := 0
	for _, d := range dismissals {
		if len(d.SignalProfile) == 0 {
			continue
		}
		var profile map[string]any
		if err := json.Unmarshal(d.SignalProfile, &profile); err != nil {
			continue
		}
		counted++
		for _, key := range []string{"sy", "du", "co", "pr", "au", "coordination_score", "signal_breadth"} {
			if v, ok := profile[key].(float64); ok {
				sums[key] += v
			}
		}
	}

	out.SampleSize = counted
	if counted > 0 {
		means := make(map[string]float64, len(sums))
		for k, v := range sums {
			means[k] = v / float64(counted)
		}
		out.MeanSignalScores = means
	} else if out.Note == "" {
		out.Note = "No dismissal carried a stored signal profile in this window, so no per-signal average could be computed."
	}

	return out, nil
}

func uuidStrings(ids []uuid.UUID) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, id.String())
	}
	return out
}

func splitNonEmpty(csv string) []string {
	if csv == "" {
		return []string{}
	}
	var out []string
	for _, part := range splitComma(csv) {
		if part != "" {
			out = append(out, part)
		}
	}
	if out == nil {
		return []string{}
	}
	return out
}

func splitComma(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}
