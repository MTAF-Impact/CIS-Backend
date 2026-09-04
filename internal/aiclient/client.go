// Package aiclient is the outbound HTTP contract with the separately-developed
// AI service.
//
// Seven outbound calls exist, all documented in docs/AI-INTEGRATION.md:
//
//  1. Matchmaking — announce a newly uploaded policy so the AI service can
//     link existing claims and generate synthetic ones.
//  2. Generate Generic Claim — the test-data button on the admin settings
//     page. The backend cannot satisfy this itself because it never writes
//     the AI-owned `claims` table.
//  3. Confirm harm — a reviewer's override of the AI harm sub-scores.
//  4. Rescore — time-based re-evaluation, without which the trend chart
//     plots a flat line.
//  5. Generate synthetic content — the only way content enters the databank
//     until a live crawler exists.
//  6. Cluster now — force a clustering pass over unclustered content.
//  7. Health — a reachability check for /health/ready.
//
// Every call degrades gracefully when AI_SERVICE_URL is unset, so the backend
// can be developed and demoed without the AI service running.
//
// # Timeouts
//
// There is no single timeout. Calls 1 and 7 return in milliseconds (the AI
// service acks and works in the background), while 2, 4, 5 and 6 run real LLM
// work inside the request and are documented at 30-60s and up. They therefore
// use cfg.Timeout and cfg.LongTimeout respectively, applied per call via the
// context rather than as one http.Client.Timeout covering both.
package aiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/cis/cis-backend/internal/config"
)

// ErrNotConfigured is returned when AI_SERVICE_URL is unset.
var ErrNotConfigured = fmt.Errorf("AI service is not configured (AI_SERVICE_URL is empty)")

// Client calls the AI service.
type Client struct {
	cfg    config.AIConfig
	client *http.Client
}

// New constructs a Client.
//
// The http.Client carries no Timeout of its own: every call sets its own
// deadline on the request context, so one shared value cannot make a fast call
// wait too long or cut a slow one short.
func New(cfg config.AIConfig) *Client {
	return &Client{cfg: cfg, client: &http.Client{}}
}

// Enabled reports whether outbound AI calls are configured.
func (c *Client) Enabled() bool { return c.cfg.Enabled() }

// MatchmakingRequest announces a new policy to the AI service.
//
// DocumentURL is a time-limited signed link the AI service can fetch to read
// the policy text. The backend deliberately does not send file bytes.
type MatchmakingRequest struct {
	PolicyID      string  `json:"policy_id"` // cis_policies.id
	Name          string  `json:"name"`
	Description   *string `json:"description,omitempty"`
	RolledOutDate string  `json:"rolled_out_date"` // YYYY-MM-DD
	Status        string  `json:"status"`
	FileName      string  `json:"file_name"`
	FileMimeType  string  `json:"file_mime_type"`
	DocumentURL   string  `json:"document_url,omitempty"`

	// Force asks the AI service to re-run its pipeline even when it already
	// holds a policy row for this policy_id, superseding that policy's prior
	// correlations rather than re-reporting them.
	//
	// Sent on /rematch and on a document replacement, where the caller's whole
	// intent is a genuine re-run; deliberately left off for the background
	// retry sweep, where a previously successful run whose callback was lost
	// should stay cheap to re-report.
	Force bool `json:"force,omitempty"`

	// CallbackURL is where the AI service reports completion. Sent only when
	// BACKEND_PUBLIC_URL is configured; otherwise it is omitted and the AI
	// service falls back to its own BACKEND_URL.
	CallbackURL string `json:"callback_url,omitempty"`
}

// MatchmakingResponse is the AI service's acknowledgement.
//
// AIPolicyID is the id the AI service used in its own `policies` table. The
// backend stores it on cis_policies.ai_policy_id as the join key for resolving
// correlated claims — it never inserts that row itself.
type MatchmakingResponse struct {
	AIPolicyID          *uuid.UUID `json:"ai_policy_id"`
	Status              string     `json:"status"`
	MatchedClaimCount   int        `json:"matched_claim_count"`
	GeneratedClaimCount int        `json:"generated_claim_count"`
	Message             string     `json:"message"`
}

// SubmitPolicy asks the AI service to run matchmaking for a policy.
func (c *Client) SubmitPolicy(ctx context.Context, req MatchmakingRequest) (*MatchmakingResponse, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}

	var out MatchmakingResponse
	if err := c.do(ctx, http.MethodPost, pathMatchmaking, c.cfg.Timeout, req, &out); err != nil {
		return nil, err
	}
	if out.Status == "" {
		out.Status = "accepted"
	}
	return &out, nil
}

// CallbackURL builds the matchmaking-result callback address for a policy, or
// "" when BACKEND_PUBLIC_URL is unset.
func (c *Client) CallbackURL(policyID uuid.UUID) string {
	if c.cfg.CallbackBaseURL == "" {
		return ""
	}
	return fmt.Sprintf("%s/api/v1/internal/policies/%s/matchmaking-result", c.cfg.CallbackBaseURL, policyID)
}

// GenerateClaimRequest asks the AI service to insert one fully-populated
// existing/generic claim for demos and testing.
type GenerateClaimRequest struct {
	ClaimType string `json:"claim_type"`
	// TopicID optionally pins the generated claim to an existing topic.
	TopicID *string `json:"topic_id,omitempty"`
}

// GenerateClaimResponse identifies the claim the AI service created.
type GenerateClaimResponse struct {
	ClaimID        *uuid.UUID `json:"claim_id"`
	ClaimStatement string     `json:"claim_statement"`
	TopicID        *uuid.UUID `json:"topic_id"`
	Message        string     `json:"message"`
}

// GenerateGenericClaim triggers the admin settings page's test-data generator.
func (c *Client) GenerateGenericClaim(ctx context.Context, req GenerateClaimRequest) (*GenerateClaimResponse, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}

	var out GenerateClaimResponse
	if err := c.do(ctx, http.MethodPost, pathGenerateClaim, c.cfg.LongTimeout, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// HarmConfirmRequest carries a reviewer's harm sub-score overrides.
//
// Every field is optional and on a 0-100 scale. An omitted field keeps the AI
// service's own classification; an entirely empty body still flips
// harm_human_confirmed, which is the "confirm as classified" case.
type HarmConfirmRequest struct {
	PublicSafety       *float64 `json:"public_safety,omitempty"`
	InstitutionalTrust *float64 `json:"institutional_trust,omitempty"`
	Economic           *float64 `json:"economic,omitempty"`
	PolicyDisruption   *float64 `json:"policy_disruption,omitempty"`
}

// ConfirmHarm records a human confirmation of a claim's harm sub-scores.
//
// The AI service recomputes harm_score -> claim_score -> final_claim_score and
// appends a score snapshot. Its response body is the AI service's own claim
// shape, which this backend deliberately does not model: the caller re-reads
// the claim from the database instead, so what it serves comes from the same
// source as every other claim read.
func (c *Client) ConfirmHarm(ctx context.Context, claimID uuid.UUID, req HarmConfirmRequest) error {
	if !c.Enabled() {
		return ErrNotConfigured
	}
	return c.do(ctx, http.MethodPatch, harmConfirmPath(claimID), c.cfg.LongTimeout, req, nil)
}

// RescoreResponse reports how many claims the AI service re-evaluated.
type RescoreResponse struct {
	ClaimsRescored int `json:"claims_rescored"`
}

// Rescore triggers a time-based re-evaluation of every existing claim.
//
// Necessary because NPR drifts purely from wall-clock time as opposing posts
// age out of the rolling window, with no new content ingested at all.
func (c *Client) Rescore(ctx context.Context) (*RescoreResponse, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}

	var out RescoreResponse
	if err := c.do(ctx, http.MethodPost, pathRescore, c.cfg.LongTimeout, struct{}{}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ClusterNowResponse reports the outcome of a forced clustering pass.
type ClusterNowResponse struct {
	ClaimsCreated         int `json:"claims_created"`
	ClaimsUpdated         int `json:"claims_updated"`
	ContentItemsClustered int `json:"content_items_clustered"`
}

// ClusterNow forces a clustering pass over any not-yet-clustered content.
func (c *Client) ClusterNow(ctx context.Context) (*ClusterNowResponse, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}

	var out ClusterNowResponse
	if err := c.do(ctx, http.MethodPost, pathClusterNow, c.cfg.LongTimeout, struct{}{}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GenerateContentRequest asks the AI service to fabricate sample content.
//
// This is the prototype stand-in for a live crawler. AutoCluster is a pointer
// so "unset" stays distinguishable from an explicit false: the AI service
// defaults it to true, and the backend does not second-guess that default.
type GenerateContentRequest struct {
	Count       int     `json:"count,omitempty"`
	TopicHint   *string `json:"topic_hint,omitempty"`
	AutoCluster *bool   `json:"auto_cluster,omitempty"`
}

// GeneratedContentItem is the subset of the AI service's content shape the
// backend reports back to the operator.
type GeneratedContentItem struct {
	ID       uuid.UUID `json:"id"`
	Text     string    `json:"text"`
	Source   string    `json:"source"`
	AuthorID *string   `json:"author_id"`
}

// GenerateContentFailure is one item the AI service could not generate.
type GenerateContentFailure struct {
	Text  string `json:"text"`
	Error string `json:"error"`
}

// GenerateContentResponse reports what the AI service ingested, and — when
// clustering ran synchronously — what it produced.
type GenerateContentResponse struct {
	Generated []GeneratedContentItem   `json:"generated"`
	Failed    []GenerateContentFailure `json:"failed"`
	// The three counts are null when auto_cluster was false.
	ClaimsCreated         *int `json:"claims_created"`
	ClaimsUpdated         *int `json:"claims_updated"`
	ContentItemsClustered *int `json:"content_items_clustered"`
}

// GenerateSampleContent fabricates content items and (by default) clusters them
// into claims.
func (c *Client) GenerateSampleContent(ctx context.Context, req GenerateContentRequest) (*GenerateContentResponse, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}

	var out GenerateContentResponse
	if err := c.do(ctx, http.MethodPost, pathGenerateContent, c.cfg.LongTimeout, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Health reports whether the AI service answers its liveness probe.
//
// Used by /health/ready to tell "configured" apart from "reachable". It is
// deliberately given a short deadline of its own: a slow AI service must not
// slow down the backend's readiness check.
func (c *Client) Health(ctx context.Context) error {
	if !c.Enabled() {
		return ErrNotConfigured
	}
	return c.do(ctx, http.MethodGet, pathHealth, 2*time.Second, nil, nil)
}

// do performs one call against the AI service.
//
// payload is encoded as JSON unless nil; out receives the decoded response
// unless nil. timeout bounds this call alone, and never extends a deadline the
// caller's context already imposes.
func (c *Client) do(ctx context.Context, method, path string, timeout time.Duration, payload, out any) error {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("encode AI request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}

	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	endpoint := c.cfg.BaseURL + "/" + strings.TrimPrefix(path, "/")
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return fmt.Errorf("build AI request: %w", err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if c.cfg.APIKey != "" {
		req.Header.Set("X-API-Key", c.cfg.APIKey)
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}

	start := time.Now()
	logAICallStart(method, path)

	res, err := c.client.Do(req)
	if err != nil {
		logAICallEnd(method, path, 0, start, err)
		return fmt.Errorf("call AI service: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		message := strings.TrimSpace(string(snippet))
		logAICallEnd(method, path, res.StatusCode, start, fmt.Errorf("%s", message))
		return fmt.Errorf("AI service returned %s: %s", res.Status, message)
	}
	logAICallEnd(method, path, res.StatusCode, start, nil)

	if out == nil {
		return nil
	}
	// A 202 with an empty body is a valid "accepted, will call back" reply.
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read AI response: %w", err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode AI response: %w", err)
	}
	return nil
}

// logAICallStart / logAICallEnd bracket every outbound call to the AI service.
// Because all nine calls funnel through do, this is the whole AI integration's
// observability: one line as a call goes out, one as it comes back with its
// status and wall time — so a slow LLM call in flight and a hung hand-off look
// different in the log, and a background matchmaking or detection run is no
// longer silent.
//
// The readiness probe (pathHealth) is polled continuously and is logged only
// when it fails.
func logAICallStart(method, path string) {
	if path == pathHealth {
		return
	}
	log.Printf("[ai] > %s %s", method, path)
}

func logAICallEnd(method, path string, status int, start time.Time, err error) {
	if path == pathHealth && err == nil {
		return
	}
	elapsed := time.Since(start).Round(time.Millisecond)
	switch {
	case err != nil && status == 0:
		log.Printf("[ai] < %s %s failed after %s: %v", method, path, elapsed, err)
	case err != nil:
		log.Printf("[ai] < %s %s %d after %s: %v", method, path, status, elapsed, err)
	default:
		log.Printf("[ai] < %s %s %d in %s", method, path, status, elapsed)
	}
}

// Timeout exposes the fast-call timeout, used when the caller builds its own
// background context around a matchmaking hand-off.
func (c *Client) Timeout() time.Duration { return c.cfg.Timeout }

// LongTimeout exposes the slow-call timeout, used when a caller needs to bound
// its own context around one of the LLM-backed calls.
func (c *Client) LongTimeout() time.Duration { return c.cfg.LongTimeout }

// MatchmakingStaleAfter exposes how long a policy may sit in "processing"
// before the retry sweep treats its matchmaking-result callback as lost.
func (c *Client) MatchmakingStaleAfter() time.Duration { return c.cfg.MatchmakingStaleAfter }

// MatchmakingMaxAttempts exposes how many times one policy's matchmaking may be
// re-queued before it stays failed (AI_MATCHMAKING_MAX_ATTEMPTS).
func (c *Client) MatchmakingMaxAttempts() int { return c.cfg.MatchmakingMaxAttempts }
