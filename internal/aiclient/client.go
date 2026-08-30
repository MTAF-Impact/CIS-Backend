// Package aiclient is the outbound HTTP contract with the separately-developed
// AI service.
//
// There are exactly two outbound calls, both documented in
// docs/AI-INTEGRATION.md:
//
//  1. Matchmaking (PRD US42) — announce a newly uploaded policy so the AI
//     service can link existing claims and generate synthetic ones.
//  2. Generate Generic Claim (PRD US33) — the F4 test-data button. The backend
//     cannot satisfy this itself because it never writes the AI-owned `claims`
//     table.
//
// Both degrade gracefully when AI_SERVICE_URL is unset, so the backend can be
// developed and demoed without the AI service running.
package aiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
func New(cfg config.AIConfig) *Client {
	return &Client{
		cfg:    cfg,
		client: &http.Client{Timeout: cfg.Timeout},
	}
}

// Enabled reports whether outbound AI calls are configured.
func (c *Client) Enabled() bool { return c.cfg.Enabled() }

// MatchmakingRequest announces a new policy to the AI service (US42).
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
	// CallbackURL is where the AI service reports completion. Optional: the AI
	// service may instead be polled or write results and call the endpoint from
	// its own configuration.
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
	if err := c.post(ctx, c.cfg.MatchmakingPath, req, &out); err != nil {
		return nil, err
	}
	if out.Status == "" {
		out.Status = "accepted"
	}
	return &out, nil
}

// GenerateClaimRequest asks the AI service to insert one fully-populated
// Existing/Generic claim for demos and testing (US33).
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

// GenerateGenericClaim triggers the F4 test-data generator.
func (c *Client) GenerateGenericClaim(ctx context.Context, req GenerateClaimRequest) (*GenerateClaimResponse, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}

	var out GenerateClaimResponse
	if err := c.post(ctx, c.cfg.GenerateClaimPath, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) post(ctx context.Context, path string, payload, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode AI request: %w", err)
	}

	endpoint := c.cfg.BaseURL + "/" + strings.TrimPrefix(path, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build AI request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.cfg.APIKey != "" {
		req.Header.Set("X-API-Key", c.cfg.APIKey)
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}

	res, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("call AI service: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return fmt.Errorf("AI service returned %s: %s", res.Status, strings.TrimSpace(string(snippet)))
	}

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

// Timeout exposes the configured per-call timeout, used when the caller builds
// its own background context.
func (c *Client) Timeout() time.Duration { return c.cfg.Timeout }
