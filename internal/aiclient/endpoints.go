package aiclient

import (
	"fmt"

	"github.com/google/uuid"
)

// The AI service's route table.
//
// These are part of the *contract* between the two services, not part of this
// deployment's configuration. A path changes when the AI team moves a route,
// which is a code change on both sides — a new request or response shape almost
// always comes with it, and no environment variable would have saved us from
// that. Keeping them here rather than in internal/config means the whole
// outbound surface is legible in one place, next to the request and response
// types it belongs to, and that a route rename is a reviewable diff instead of
// a silent difference between two deployments' .env files.
//
// What *is* environment-specific — the base URL, the API key, the timeouts —
// stays in config.AIConfig.
//
// Cross-referenced against the AI service's API_REFERENCE.md. When it changes,
// change it here and update docs/AI-INTEGRATION.md's endpoint table in the same
// commit.
const (
	// pathMatchmaking announces a newly uploaded policy so the AI service can
	// correlate it against claims. Acked in milliseconds; the work happens in
	// the AI service's background.
	pathMatchmaking = "/api/v1/matchmaking/policies"

	// pathGenerateClaim inserts one fully-populated Existing claim for demos.
	pathGenerateClaim = "/api/v1/claims/generate-generic"

	// pathRescore re-evaluates every claim's score against the wall clock.
	// Called hourly, ahead of the snapshot capture.
	pathRescore = "/api/v1/claims/rescore"

	// pathClusterNow forces a clustering pass over unclustered content.
	pathClusterNow = "/api/v1/claims/cluster-now"

	// pathGenerateContent fabricates sample content items. The stand-in for a
	// live crawler.
	pathGenerateContent = "/api/v1/ingest/generate-synthetic"

	// pathHealth is the AI service's liveness probe, used by /health/ready to
	// tell "configured" apart from "reachable".
	pathHealth = "/health"
)

// harmConfirmPath is the one path with a parameter in it: a reviewer's
// override of a claim's harm sub-scores.
func harmConfirmPath(claimID uuid.UUID) string {
	return fmt.Sprintf("/api/v1/claims/%s/harm/confirm", claimID)
}
