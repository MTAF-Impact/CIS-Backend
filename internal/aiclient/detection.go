package aiclient

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// F5 — the eighth outbound call: asking the AI service to run the
// Coordinated-Network Detector.
//
// # Why this is a hand-off and not a synchronous computation
//
// PRD 10.5.8 sets the performance target at "a 5,000-account, 7-day run
// completes in under 10 minutes on commodity hardware". That is the worst case
// the system is allowed to accept, and ten minutes is far beyond any HTTP
// request this backend should hold open. So the call announces the run, the AI
// service acknowledges in milliseconds, and the work happens in its background —
// the same shape as Flow 1's matchmaking hand-off, for the same reason.
//
// The backend then reads the results out of the detection tables. It never
// polls the AI service: detection_run.status is the source of truth, and it is
// a database row both services can see.
//
// # Why the parameters are sent rather than read
//
// PRD US62 requires that changing a parameter never retroactively alters a
// stored detection: every run records the parameter set in force at execution
// time. Sending them with the request is what makes that true even if an admin
// edits the configuration while the run is in flight.

// pathDetectionRun starts a Coordinated-Network Detector run (PRD 10.5.8).
const pathDetectionRun = "/api/v1/detection/runs"

// pathDetectionPurge asks the AI service to purge expired evidence snapshots
// (PRD 10.9.1 rule 7).
//
// The backend identifies which snapshots have expired — it can see both the
// retention date and whether a report was generated — but the rows are AI-owned,
// so the deletion itself is the pipeline's to perform.
const pathDetectionPurge = "/api/v1/detection/snapshots/purge"

// DetectionRunRequest asks the AI service to run detection over a claim scope.
type DetectionRunRequest struct {
	ClaimIDs []uuid.UUID `json:"claim_ids"`
	// TriggerSource is scheduled | velocity | on_demand (PRD 10.5.8's three
	// execution paths), recorded on the run so an analyst can tell an automated
	// detection from one somebody asked for.
	TriggerSource string `json:"trigger_source"`
	// WindowStart and WindowEnd are computed by the backend from W, so that
	// consecutive scheduled runs overlap by 50% of the window as PRD 10.5.1
	// requires. Leaving the window to the AI service would put that rule in the
	// one place the backend cannot enforce it.
	WindowStart time.Time `json:"window_start"`
	WindowEnd   time.Time `json:"window_end"`
	// Parameters is the full detector configuration in force right now.
	Parameters any `json:"parameters"`
	// Exclusions carries the declared-coordination allowlist and the
	// common-phrase list. They are backend-owned and pipeline-read, so they
	// travel with the request rather than being fetched by the AI service.
	Exclusions any `json:"exclusions"`
}

// DetectionRunResponse is the AI service's acknowledgement.
//
// RunID is the id it wrote into detection_run. The backend stores nothing from
// this response beyond echoing the id back to the caller: the run's state lives
// in the shared table, and duplicating it here would create a second answer.
type DetectionRunResponse struct {
	RunID  uuid.UUID `json:"run_id"`
	Status string    `json:"status"`
}

// TriggerDetection asks the AI service to start a detection run.
//
// Uses the short timeout deliberately: the AI service acknowledges and works in
// the background, so a slow response here means the hand-off failed, not that
// the detection is slow.
func (c *Client) TriggerDetection(ctx context.Context, req DetectionRunRequest) (*DetectionRunResponse, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}

	var out DetectionRunResponse
	if err := c.do(ctx, http.MethodPost, pathDetectionRun, c.cfg.Timeout, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PurgeSnapshotsRequest names the networks whose evidence snapshots have passed
// their retention date.
//
// The list is computed by the backend rather than by a TTL on the AI side,
// because PRD 10.9.1 rule 7's exception depends on a backend-owned table: a
// snapshot is retained as long as any report generated from it, and
// cis_network_reports is where that is recorded. A blanket TTL delete would
// eventually purge the evidence under a report already submitted to a platform,
// and a report whose evidence has been purged is worthless as evidence.
type PurgeSnapshotsRequest struct {
	NetworkIDs []uuid.UUID `json:"network_ids"`
}

// PurgeSnapshotsResponse reports how many snapshots were purged.
type PurgeSnapshotsResponse struct {
	SnapshotsPurged int `json:"snapshots_purged"`
}

// PurgeExpiredSnapshots asks the AI service to delete the named snapshots.
func (c *Client) PurgeExpiredSnapshots(ctx context.Context, req PurgeSnapshotsRequest) (*PurgeSnapshotsResponse, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}

	var out PurgeSnapshotsResponse
	if err := c.do(ctx, http.MethodPost, pathDetectionPurge, c.cfg.Timeout, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
