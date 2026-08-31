// Package scheduler runs the background jobs the PRD requires.
//
// Three jobs matter:
//
//   - Policy rollout (US41): a "Not Rolled Out" policy's date eventually
//     arrives, so status must be re-evaluated on a schedule, not only at
//     creation time.
//   - Score snapshots: the F3 chart plots FinalClaimScore over time (US27), but
//     the AI service only stores the current value. This job asks the AI
//     service to re-evaluate scores, then copies them into the backend-owned
//     snapshot table to build that history.
//   - Matchmaking retry: a policy whose AI hand-off failed, or whose result
//     callback was lost, is stranded on a "Processing" badge until something
//     re-queues it. Nothing else does.
//
// The backend owns cron for all three. The AI service has no scheduler at all —
// its background work is exclusively request-scoped — and the rescore must
// happen before the snapshot, which is trivially ordered when one process
// drives both.
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
	admin    *service.AdminService
}

// New constructs a Scheduler.
func New(
	cfg config.CronConfig,
	policies *service.PolicyService,
	alerts *service.AlertService,
	admin *service.AdminService,
) *Scheduler {
	return &Scheduler{
		cron:     cron.New(cron.WithLocation(time.UTC)),
		cfg:      cfg,
		policies: policies,
		alerts:   alerts,
		admin:    admin,
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
	if _, err := s.cron.AddFunc(s.cfg.MatchmakingRetrySpec, s.runMatchmakingRetry); err != nil {
		return err
	}

	s.cron.Start()
	log.Printf("[cron] started: policy rollout %q, score snapshot %q, matchmaking retry %q",
		s.cfg.PolicyRolloutSpec, s.cfg.ScoreSnapshotSpec, s.cfg.MatchmakingRetrySpec)

	// Run once at boot so a server that was down over a scheduled window catches
	// up instead of waiting for the next tick. The retry sweep in particular
	// matters here: a crash mid-matchmaking is exactly what strands a policy.
	go s.runPolicyRollout()
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

// runPolicyRollout flips policies whose rolled-out date has arrived (US41).
func (s *Scheduler) runPolicyRollout() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	updated, err := s.policies.RefreshRolloutStatuses(ctx)
	if err != nil {
		log.Printf("[cron] policy rollout job failed: %v", err)
	} else if updated > 0 {
		log.Printf("[cron] flipped %d policies to rolled_out", updated)
	}
}

// runMatchmakingRetry re-queues matchmaking jobs that failed or whose result
// callback never arrived.
//
// It runs on its own frequent schedule rather than riding along with the daily
// rollout job: a stranded policy shows a spinning badge and empty claim lists
// for as long as it takes to notice, and a day is too long.
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

// runScoreSnapshot re-evaluates scores, captures the result for every watched
// claim, and prunes history beyond the retention window.
//
// The rescore comes first, and the ordering is the whole point. Scores move with
// wall-clock time alone — NPR drifts as opposing posts age out of the rolling
// window, which changes the discount factor and so final_claim_score — but
// nothing recomputes them on a schedule: the AI service has no cron of its own,
// and clustering only runs when content arrives. Capturing without rescoring
// first would copy the same number into the history every hour and draw US27's
// trend chart as a horizontal line by construction.
//
// A failed rescore is logged and the capture still runs: stale scores are worth
// more in the chart than a gap.
func (s *Scheduler) runScoreSnapshot() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if rescored, err := s.admin.RescoreIfEnabled(ctx); err != nil {
		log.Printf("[cron] AI rescore failed, capturing existing scores anyway: %v", err)
	} else if rescored > 0 {
		log.Printf("[cron] AI service rescored %d claims", rescored)
	}

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
