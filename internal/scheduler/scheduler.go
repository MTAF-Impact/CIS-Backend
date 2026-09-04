// Package scheduler runs the background jobs the system requires.
//
// Four jobs matter:
//
//   - Score snapshots: the Alert page's chart plots FinalClaimScore over
//     time, but the AI service only stores the current value, so this job
//     copies it into the backend-owned snapshot table to build that history.
//     It captures — it does not ask the AI service to rescore first; see
//     runScoreSnapshot.
//   - Matchmaking retry: a policy whose AI hand-off failed, or whose result
//     callback was lost, is stranded on a "Processing" badge until something
//     re-queues it. Nothing else does.
//   - Detection tick: the scheduled sweep over Active claims and the
//     velocity-triggered runs. The tick is not the cadence — see
//     runDetection.
//   - Snapshot retention: purges evidence snapshots past their horizon,
//     except where a report was generated from them.
//
// The backend owns cron for all four. The AI service has no scheduler at all —
// its background work is exclusively request-scoped. The Coordinated-Network
// Detector's runs are dispatched from here because the scope rules (Active
// Existing claims only) and the window arithmetic live on this side.
//
// # What used to be here
//
// A fifth job flipped a policy from "Not Rolled Out" to "Rolled Out" once its
// date arrived. It was removed: a rolled-out date is a plan, and a job
// comparing it against the clock cannot tell a policy that actually launched
// from one that slipped. Rollout is now an operator's decision
// (PUT /api/v1/policies/:id/status, PolicyService.SetStatus).
package scheduler

import (
	"context"
	"log"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/cis/cis-backend/internal/config"
	"github.com/cis/cis-backend/internal/service"
)

// Scheduler owns the cron runner.
type Scheduler struct {
	cron      *cron.Cron
	cfg       config.CronConfig
	policies  *service.PolicyService
	alerts    *service.AlertService
	settings  *service.SettingService
	detection *service.DetectionService
}

// New constructs a Scheduler.
func New(
	cfg config.CronConfig,
	policies *service.PolicyService,
	alerts *service.AlertService,
	settings *service.SettingService,
	detection *service.DetectionService,
) *Scheduler {
	return &Scheduler{
		cron:      cron.New(cron.WithLocation(time.UTC)),
		cfg:       cfg,
		policies:  policies,
		alerts:    alerts,
		settings:  settings,
		detection: detection,
	}
}

// Start registers and begins the jobs. It is a no-op when CRON_ENABLED=false.
func (s *Scheduler) Start() error {
	if !s.cfg.Enabled {
		log.Println("[cron] disabled (CRON_ENABLED=false)")
		return nil
	}

	if _, err := s.cron.AddFunc(s.cfg.ScoreSnapshotSpec, s.runScoreSnapshot); err != nil {
		return err
	}
	if _, err := s.cron.AddFunc(s.cfg.MatchmakingRetrySpec, s.runMatchmakingRetry); err != nil {
		return err
	}
	if _, err := s.cron.AddFunc(s.cfg.DetectionSpec, s.runDetection); err != nil {
		return err
	}
	if _, err := s.cron.AddFunc(s.cfg.SnapshotRetentionSpec, s.runSnapshotRetention); err != nil {
		return err
	}

	s.cron.Start()
	log.Printf("[cron] started: score snapshot %q, matchmaking retry %q, "+
		"detection tick %q, snapshot retention %q",
		s.cfg.ScoreSnapshotSpec, s.cfg.MatchmakingRetrySpec,
		s.cfg.DetectionSpec, s.cfg.SnapshotRetentionSpec)

	// Run once at boot so a server that was down over a scheduled window catches
	// up instead of waiting for the next tick. The retry sweep is the one that
	// matters here: a crash mid-matchmaking is exactly what strands a policy.
	go s.runMatchmakingRetry()
	return nil
}

// Stop halts the scheduler, waiting for running jobs to finish.
func (s *Scheduler) Stop() {
	if !s.cfg.Enabled {
		return
	}
	ctx := s.cron.Stop()
	select {
	case <-ctx.Done():
	case <-time.After(10 * time.Second):
		log.Println("[cron] timed out waiting for jobs to finish")
	}
}

// runMatchmakingRetry re-queues matchmaking jobs that failed or whose result
// callback never arrived.
//
// It runs on a frequent schedule because a stranded policy shows a spinning
// badge and empty claim lists for as long as it takes to notice, and a day is
// too long.
func (s *Scheduler) runMatchmakingRetry() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	retried, err := s.policies.RetryStuckMatchmaking(ctx)
	if err != nil {
		log.Printf("[cron] matchmaking retry failed: %v", err)
	} else if retried > 0 {
		log.Printf("[cron] re-queued %d stuck matchmaking jobs", retried)
	}
}

// runScoreSnapshot captures the current score of every watched claim, evaluates
// threshold crossings, and prunes history beyond the retention window.
//
// # Why it no longer asks the AI service to rescore first
//
// It used to, on the reasoning that scores drift with wall-clock time alone and
// nothing recomputed them on a schedule. That reasoning no longer holds: the AI
// pipeline rescores a claim whenever its inputs move — every ingest that
// attaches a statement re-runs clustering, which recomputes R/V/F/H/EI, NPR and
// the discount for every claim it touched, and a harm confirmation re-runs the
// same pass for that claim (CIS-AI docs/SCORING.md, "When scores are
// (re)computed"). Every path that can change a score already recomputes it.
//
// So this job's remaining question is not "are the scores current" but "has the
// current value been recorded", and firing an hourly full-repository rescore to
// answer it spent an LLM-backed pass on claims whose inputs had not moved.
// Capturing is cheap, is the only thing the Alert page's chart actually
// needs, and leaves rescoring where it belongs: with the events that change
// a score. A rescore is still available on demand
// (POST /api/v1/admin/rescore).
func (s *Scheduler) runScoreSnapshot() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	count, err := s.alerts.CaptureSnapshots(ctx)
	if err != nil {
		log.Printf("[cron] score snapshot job failed: %v", err)
		return
	}
	if count > 0 {
		log.Printf("[cron] captured %d claim score snapshots", count)
	}

	// A crossing is a change between two evaluations, so it can only be
	// detected where the scores have just been read. Running it here — right
	// after the capture, not on a clock of its own — is what makes "on each
	// score refresh" true rather than approximately true.
	if crossings, err := s.alerts.EvaluateCrossings(ctx); err != nil {
		log.Printf("[cron] threshold crossing evaluation failed: %v", err)
	} else if crossings > 0 {
		log.Printf("[cron] %d watched claims crossed the alert threshold", crossings)
	}

	retention := s.settings.ScoreSnapshotRetention(ctx)
	if deleted, err := s.alerts.PruneSnapshots(ctx, retention); err != nil {
		log.Printf("[cron] snapshot pruning failed: %v", err)
	} else if deleted > 0 {
		log.Printf("[cron] pruned %d score snapshots older than %s", deleted, retention)
	}
}

// runDetection fires the scheduled sweep and the velocity trigger.
//
// The tick is deliberately more frequent than the detection cadence. The
// cadence is a detector setting an admin edits in Admin Settings (1-24 h)
// and a cron spec is fixed at boot, so the two cannot be the same thing:
// this job asks "is a run due?" every tick and DetectionService.RunScheduled
// answers from the current setting. An admin who tightens the cadence gets
// the new one on the next tick rather than on the next deploy.
//
// The timeout is generous because a run over 200 claims is a large hand-off,
// but it only covers the dispatch — the 10-minute processing budget is the
// AI service's own, and the pipeline runs asynchronously on its side.
//
// A failing scheduled sweep does not stop the velocity trigger: they answer
// different questions, and the spike is the one that cannot wait.
func (s *Scheduler) runDetection() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if count, err := s.detection.RunScheduled(ctx); err != nil {
		log.Printf("[cron] scheduled detection sweep failed: %v", err)
	} else if count > 0 {
		log.Printf("[cron] dispatched a detection run over %d active claims", count)
	}

	if count, err := s.detection.RunVelocityTriggered(ctx); err != nil {
		log.Printf("[cron] velocity-triggered detection failed: %v", err)
	} else if count > 0 {
		log.Printf("[cron] dispatched a velocity-triggered detection run over %d claims", count)
	}
}

// runSnapshotRetention purges evidence snapshots past their retention horizon.
//
// Not a blanket TTL delete: a snapshot a report was generated from lives as long
// as the report, because a report whose evidence has been purged is worthless as
// evidence. The exception is applied in DetectionService, which is the only side
// that can see cis_network_reports.
func (s *Scheduler) runSnapshotRetention() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	purged, err := s.detection.PurgeExpiredSnapshots(ctx)
	if err != nil {
		log.Printf("[cron] evidence snapshot retention failed: %v", err)
		return
	}
	if purged > 0 {
		log.Printf("[cron] purged %d expired evidence snapshots", purged)
	}
}
