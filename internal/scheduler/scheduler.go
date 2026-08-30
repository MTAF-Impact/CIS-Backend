// Package scheduler runs the background jobs the PRD requires.
//
// Two jobs matter:
//
//   - Policy rollout (US41): a "Not Rolled Out" policy's date eventually
//     arrives, so status must be re-evaluated on a schedule, not only at
//     creation time.
//   - Score snapshots: the F3 chart plots FinalClaimScore over time (US27), but
//     the AI service only stores the current value. This job copies scores into
//     the backend-owned snapshot table to build that history.
package scheduler

import (
	"context"
	"log"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/cis/cis-backend/internal/config"
	"github.com/cis/cis-backend/internal/service"
)

// SnapshotRetention is how long score history is kept before pruning.
const SnapshotRetention = 400 * 24 * time.Hour

// Scheduler owns the cron runner.
type Scheduler struct {
	cron     *cron.Cron
	cfg      config.CronConfig
	policies *service.PolicyService
	alerts   *service.AlertService
}

// New constructs a Scheduler.
func New(cfg config.CronConfig, policies *service.PolicyService, alerts *service.AlertService) *Scheduler {
	return &Scheduler{
		cron:     cron.New(cron.WithLocation(time.UTC)),
		cfg:      cfg,
		policies: policies,
		alerts:   alerts,
	}
}

// Start registers and begins the jobs. It is a no-op when CRON_ENABLED=false.
func (s *Scheduler) Start() error {
	if !s.cfg.Enabled {
		log.Println("[cron] disabled (CRON_ENABLED=false)")
		return nil
	}

	if _, err := s.cron.AddFunc(s.cfg.PolicyRolloutSpec, s.runPolicyRollout); err != nil {
		return err
	}
	if _, err := s.cron.AddFunc(s.cfg.ScoreSnapshotSpec, s.runScoreSnapshot); err != nil {
		return err
	}

	s.cron.Start()
	log.Printf("[cron] started: policy rollout %q, score snapshot %q",
		s.cfg.PolicyRolloutSpec, s.cfg.ScoreSnapshotSpec)

	// Run both once at boot so a server that was down over a scheduled window
	// catches up instead of waiting for the next tick.
	go s.runPolicyRollout()
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

// runPolicyRollout flips policies whose rolled-out date has arrived (US41) and
// retries any matchmaking job that never completed.
func (s *Scheduler) runPolicyRollout() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	updated, err := s.policies.RefreshRolloutStatuses(ctx)
	if err != nil {
		log.Printf("[cron] policy rollout job failed: %v", err)
	} else if updated > 0 {
		log.Printf("[cron] flipped %d policies to rolled_out", updated)
	}

	retried, err := s.policies.RetryStuckMatchmaking(ctx)
	if err != nil {
		log.Printf("[cron] matchmaking retry failed: %v", err)
	} else if retried > 0 {
		log.Printf("[cron] re-queued %d stuck matchmaking jobs", retried)
	}
}

// runScoreSnapshot captures the current scores of every watched claim and
// prunes history beyond the retention window.
func (s *Scheduler) runScoreSnapshot() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	count, err := s.alerts.CaptureSnapshots(ctx)
	if err != nil {
		log.Printf("[cron] score snapshot job failed: %v", err)
		return
	}
	if count > 0 {
		log.Printf("[cron] captured %d claim score snapshots", count)
	}

	if deleted, err := s.alerts.PruneSnapshots(ctx, SnapshotRetention); err != nil {
		log.Printf("[cron] snapshot pruning failed: %v", err)
	} else if deleted > 0 {
		log.Printf("[cron] pruned %d expired score snapshots", deleted)
	}
}
