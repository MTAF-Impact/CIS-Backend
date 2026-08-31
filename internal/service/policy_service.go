package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/google/uuid"

	"github.com/cis/cis-backend/internal/aiclient"
	"github.com/cis/cis-backend/internal/config"
	"github.com/cis/cis-backend/internal/dto"
	"github.com/cis/cis-backend/internal/models"
	"github.com/cis/cis-backend/internal/pkg/apperr"
	"github.com/cis/cis-backend/internal/repository"
	"github.com/cis/cis-backend/internal/storage"
)

// maxMatchmakingAttempts caps how many times a policy's AI matchmaking job is
// retried before it stays failed and needs a manual /rematch.
const maxMatchmakingAttempts = 3

// PolicyService serves F2, the Public Policy Bank.
//
// It owns cis_policies outright. It never inserts into the AI service's
// `policies` table — the AI service creates its own record during matchmaking
// and reports the id back, which is stored as cis_policies.ai_policy_id.
type PolicyService struct {
	policies *repository.PolicyRepository
	claims   *repository.ClaimRepository
	alerts   *repository.AlertRepository
	store    storage.Storage
	ai       *aiclient.Client
	appCfg   config.AppConfig
}

// NewPolicyService constructs a PolicyService.
func NewPolicyService(
	policies *repository.PolicyRepository,
	claims *repository.ClaimRepository,
	alerts *repository.AlertRepository,
	store storage.Storage,
	ai *aiclient.Client,
	appCfg config.AppConfig,
) *PolicyService {
	return &PolicyService{policies: policies, claims: claims, alerts: alerts, store: store, ai: ai, appCfg: appCfg}
}

// ListPoliciesQuery is the normalized F2 list input.
type ListPoliciesQuery struct {
	Years  []int
	Search string
	Status string
	Page   int
	Limit  int
}

// List returns a page of policy cards ordered per US35.
func (s *PolicyService) List(ctx context.Context, q ListPoliciesQuery) ([]dto.PolicyCard, int64, dto.PageParams, error) {
	page := dto.NormalizePage(q.Page, q.Limit)

	rows, total, err := s.policies.List(ctx, repository.PolicyFilter{
		Years:  q.Years,
		Search: q.Search,
		Status: q.Status,
		Limit:  page.Limit,
		Offset: page.Offset(),
	})
	if err != nil {
		return nil, 0, page, apperr.Internal("could not load policies").Wrap(err)
	}

	cards := make([]dto.PolicyCard, 0, len(rows))
	for _, row := range rows {
		cards = append(cards, toPolicyCard(row))
	}
	return cards, total, page, nil
}

// Years returns the distinct rolled-out years for the US34 filter chips.
func (s *PolicyService) Years(ctx context.Context) (*dto.PolicyYearsResponse, error) {
	years, err := s.policies.ListYears(ctx)
	if err != nil {
		return nil, apperr.Internal("could not load policy years").Wrap(err)
	}
	if years == nil {
		years = []int{}
	}
	return &dto.PolicyYearsResponse{Years: years}, nil
}

// Detail returns a policy with its correlated claim lists (US39).
func (s *PolicyService) Detail(ctx context.Context, id uuid.UUID) (*dto.PolicyDetail, error) {
	row, err := s.policies.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, apperr.NotFound("policy not found")
		}
		return nil, apperr.Internal("could not load policy").Wrap(err)
	}

	detail := &dto.PolicyDetail{
		PolicyCard:        toPolicyCard(*row),
		Description:       row.Description,
		ExistingClaims:    []dto.ClaimCard{},
		NonExistingClaims: []dto.ClaimCard{},
	}

	// Correlations only exist once the AI service has reported the policy id it
	// used. Until then the lists are legitimately empty and the card shows the
	// "Processing" badge.
	if row.AIPolicyID == nil {
		return detail, nil
	}

	existing, err := s.claimsForPolicy(ctx, *row.AIPolicyID, models.ClaimTypeExisting)
	if err != nil {
		return nil, err
	}
	nonExisting, err := s.claimsForPolicy(ctx, *row.AIPolicyID, models.ClaimTypeNonExisting)
	if err != nil {
		return nil, err
	}

	detail.ExistingClaims = existing
	detail.NonExistingClaims = nonExisting
	return detail, nil
}

// claimsForPolicy loads one side of the US39 claim lists.
//
// The cards are built with the same shape as F1's so the frontend can reuse the
// identical component, including the bell-icon state for Existing claims.
func (s *PolicyService) claimsForPolicy(ctx context.Context, aiPolicyID uuid.UUID, claimType string) ([]dto.ClaimCard, error) {
	sortBy := repository.SortByScore
	if claimType == models.ClaimTypeNonExisting {
		sortBy = repository.SortByCreatedAt
	}

	rows, err := s.claims.ListClaims(ctx, repository.ClaimFilter{
		ClaimType: claimType,
		PolicyIDs: []uuid.UUID{aiPolicyID},
		SortBy:    sortBy,
		Limit:     dto.MaxLimit,
	})
	if err != nil {
		return nil, apperr.Internal("could not load correlated claims").Wrap(err)
	}

	ids := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		if models.NormalizeClaimType(row.ClaimType) == models.ClaimTypeExisting {
			ids = append(ids, row.ID)
		}
	}

	counts, err := s.claims.CountStatementsByClaim(ctx, ids)
	if err != nil {
		return nil, apperr.Internal("could not count claim statements").Wrap(err)
	}
	alerted, err := s.alerts.AlertedClaimIDs(ctx, ids)
	if err != nil {
		return nil, apperr.Internal("could not resolve alert state").Wrap(err)
	}

	cards := make([]dto.ClaimCard, 0, len(rows))
	for _, row := range rows {
		cards = append(cards, buildClaimCard(row, counts, alerted))
	}
	return cards, nil
}

// CreatePolicyInput carries the validated "Add Public Policy" submission.
type CreatePolicyInput struct {
	Name          string
	Description   *string
	RolledOutDate time.Time
	FileName      string
	MimeType      string
	FileSize      int64
	File          io.Reader
	CreatedBy     *uuid.UUID
}

// Create registers a policy: stores the document, derives its rollout status,
// persists the record, and kicks off AI matchmaking in the background
// (US40, US41, US42).
func (s *PolicyService) Create(ctx context.Context, in CreatePolicyInput) (*dto.PolicyCard, error) {
	policyID := uuid.New()
	objectPath := storage.BuildObjectPath(policyID, in.FileName)

	object, err := s.store.Upload(ctx, objectPath, in.File, in.FileSize, in.MimeType)
	if err != nil {
		return nil, apperr.Internal("could not store the policy document").Wrap(err)
	}

	size := in.FileSize
	if size <= 0 {
		size = object.Size
	}

	now := time.Now().UTC()
	policy := &models.CISPolicy{
		ID:            policyID,
		Name:          in.Name,
		Description:   in.Description,
		RolledOutDate: in.RolledOutDate,
		// US41: status is derived automatically at creation, never entered by
		// the user, and re-evaluated daily by the cron job.
		Status:        models.DeriveStatus(in.RolledOutDate, now),
		FileName:      in.FileName,
		FilePath:      object.Path,
		FileMimeType:  object.MimeType,
		FileSizeBytes: size,
		CreatedBy:     in.CreatedBy,
	}

	// When no AI service is configured, mark the job skipped rather than
	// pending so the card does not spin on a "Processing" badge forever.
	if s.ai.Enabled() {
		policy.ProcessingStatus = models.ProcessingPending
	} else {
		policy.ProcessingStatus = models.ProcessingSkipped
	}

	if err := s.policies.Create(ctx, policy); err != nil {
		// Roll back the upload so a failed insert does not orphan a document.
		if delErr := s.store.Delete(context.Background(), object.Path); delErr != nil {
			log.Printf("[policy] orphaned document %s after failed insert: %v", object.Path, delErr)
		}
		return nil, apperr.Internal("could not save the policy").Wrap(err)
	}

	if s.ai.Enabled() {
		// A brand-new policy the AI service has never seen: nothing to force.
		s.startMatchmaking(policy.ID, false)
	}

	row, err := s.policies.FindByID(ctx, policy.ID)
	if err != nil {
		return nil, apperr.Internal("could not reload the created policy").Wrap(err)
	}
	card := toPolicyCard(*row)
	return &card, nil
}

// startMatchmaking runs the US42 handoff in the background.
//
// The upload response must not block on the AI service, which is exactly why
// the card shows a "Processing" badge until this finishes.
//
// force is passed through to the AI service, which otherwise short-circuits a
// repeat submission for a policy it already knows and re-reports the previous
// run's counts. See MatchmakingRequest.Force.
func (s *PolicyService) startMatchmaking(policyID uuid.UUID, force bool) {
	go func() {
		// A fresh context: the HTTP request that triggered this is already done.
		ctx, cancel := context.WithTimeout(context.Background(), s.ai.Timeout()+30*time.Second)
		defer cancel()

		if err := s.runMatchmaking(ctx, policyID, force); err != nil {
			log.Printf("[policy] matchmaking failed for %s: %v", policyID, err)
		}
	}()
}

func (s *PolicyService) runMatchmaking(ctx context.Context, policyID uuid.UUID, force bool) error {
	row, err := s.policies.FindByID(ctx, policyID)
	if err != nil {
		return fmt.Errorf("load policy: %w", err)
	}

	if err := s.policies.Update(ctx, policyID, map[string]any{
		"processing_status":   models.ProcessingInProgress,
		"processing_attempts": row.ProcessingAttempts + 1,
		"processing_error":    nil,
	}); err != nil {
		return fmt.Errorf("mark processing: %w", err)
	}

	// Give the AI service a signed link to read the document rather than
	// shipping bytes over the wire.
	documentURL, _, signErr := s.store.SignedURL(ctx, row.FilePath)
	if signErr != nil {
		log.Printf("[policy] could not sign document for %s, continuing without it: %v", policyID, signErr)
	}

	res, err := s.ai.SubmitPolicy(ctx, aiclient.MatchmakingRequest{
		PolicyID:      row.ID.String(),
		Name:          row.Name,
		Description:   row.Description,
		RolledOutDate: row.RolledOutDate.Format("2006-01-02"),
		Status:        row.Status,
		FileName:      row.FileName,
		FileMimeType:  row.FileMimeType,
		DocumentURL:   documentURL,
		Force:         force,
		CallbackURL:   s.ai.CallbackURL(row.ID),
	})
	if err != nil {
		message := err.Error()
		if updateErr := s.policies.Update(ctx, policyID, map[string]any{
			"processing_status": models.ProcessingFailed,
			"processing_error":  &message,
		}); updateErr != nil {
			log.Printf("[policy] could not record matchmaking failure for %s: %v", policyID, updateErr)
		}
		return err
	}

	updates := map[string]any{"processing_error": nil}
	if res.AIPolicyID != nil {
		updates["ai_policy_id"] = *res.AIPolicyID
	}

	// A "processing" reply means the AI service accepted the job and will call
	// the result endpoint when it finishes, so the badge stays on.
	if res.Status == "processing" || res.Status == "accepted" {
		updates["processing_status"] = models.ProcessingInProgress
	} else {
		now := time.Now().UTC()
		updates["processing_status"] = models.ProcessingCompleted
		updates["processed_at"] = &now
	}

	if err := s.policies.Update(ctx, policyID, updates); err != nil {
		return fmt.Errorf("record matchmaking result: %w", err)
	}
	return nil
}

// Rematch re-runs matchmaking for a policy, used after a failure.
func (s *PolicyService) Rematch(ctx context.Context, id uuid.UUID) (*dto.PolicyProcessingStatus, error) {
	row, err := s.policies.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, apperr.NotFound("policy not found")
		}
		return nil, apperr.Internal("could not load policy").Wrap(err)
	}

	if !s.ai.Enabled() {
		return nil, apperr.Unavailable(
			"AI matchmaking is not configured; set AI_SERVICE_URL to enable it")
	}
	if row.ProcessingStatus == models.ProcessingInProgress {
		return nil, apperr.Conflict("matchmaking is already running for this policy")
	}

	if err := s.policies.Update(ctx, id, map[string]any{
		"processing_status":   models.ProcessingPending,
		"processing_attempts": 0,
		"processing_error":    nil,
	}); err != nil {
		return nil, apperr.Internal("could not queue matchmaking").Wrap(err)
	}

	// An operator pressing Rematch wants the pipeline actually re-run, not the
	// previous run's counts read back to them.
	s.startMatchmaking(id, true)
	return s.ProcessingStatus(ctx, id)
}

// ApplyMatchmakingResult records the AI service's callback (US42).
func (s *PolicyService) ApplyMatchmakingResult(ctx context.Context, id uuid.UUID, req dto.MatchmakingResultRequest) (*dto.PolicyProcessingStatus, error) {
	if _, err := s.policies.FindByID(ctx, id); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, apperr.NotFound("policy not found")
		}
		return nil, apperr.Internal("could not load policy").Wrap(err)
	}

	updates := map[string]any{}

	if req.AIPolicyID != nil {
		aiID, err := uuid.Parse(*req.AIPolicyID)
		if err != nil {
			return nil, apperr.Unprocessable("ai_policy_id must be a valid UUID")
		}
		updates["ai_policy_id"] = aiID
	}

	switch req.Status {
	case "completed":
		now := time.Now().UTC()
		updates["processing_status"] = models.ProcessingCompleted
		updates["processed_at"] = &now
		updates["processing_error"] = nil
	case "failed":
		updates["processing_status"] = models.ProcessingFailed
		updates["processing_error"] = req.Error
	default:
		updates["processing_status"] = models.ProcessingInProgress
	}

	if err := s.policies.Update(ctx, id, updates); err != nil {
		return nil, apperr.Internal("could not record matchmaking result").Wrap(err)
	}
	return s.ProcessingStatus(ctx, id)
}

// ProcessingStatus returns the badge state the F2 card polls (US42).
func (s *PolicyService) ProcessingStatus(ctx context.Context, id uuid.UUID) (*dto.PolicyProcessingStatus, error) {
	row, err := s.policies.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, apperr.NotFound("policy not found")
		}
		return nil, apperr.Internal("could not load policy").Wrap(err)
	}

	status := &dto.PolicyProcessingStatus{
		PolicyID:         row.ID.String(),
		ProcessingStatus: row.ProcessingStatus,
		IsProcessing:     isProcessing(row.ProcessingStatus),
		Attempts:         row.ProcessingAttempts,
		ProcessedAt:      row.ProcessedAt,
		ProcessingError:  row.ProcessingError,
		LinkedClaimCount: row.LinkedClaimCount,
	}
	if row.AIPolicyID != nil {
		id := row.AIPolicyID.String()
		status.AIPolicyID = &id
	}
	return status, nil
}

// Update applies an edit to a policy's metadata, re-deriving the rollout status
// when the date changes (US41).
func (s *PolicyService) Update(ctx context.Context, id uuid.UUID, req dto.UpdatePolicyRequest) (*dto.PolicyCard, error) {
	if _, err := s.policies.FindByID(ctx, id); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, apperr.NotFound("policy not found")
		}
		return nil, apperr.Internal("could not load policy").Wrap(err)
	}

	updates := map[string]any{}
	if req.Name != nil {
		updates["name"] = *req.Name
	}
	if req.Description != nil {
		updates["description"] = *req.Description
	}
	if req.RolledOutDate != nil {
		date, err := time.Parse("2006-01-02", *req.RolledOutDate)
		if err != nil {
			return nil, apperr.Unprocessable("rolled_out_date must be a YYYY-MM-DD date")
		}
		updates["rolled_out_date"] = date
		updates["status"] = models.DeriveStatus(date, time.Now().UTC())
	}

	if len(updates) == 0 {
		return nil, apperr.BadRequest("no updatable fields were provided")
	}
	if err := s.policies.Update(ctx, id, updates); err != nil {
		return nil, apperr.Internal("could not update the policy").Wrap(err)
	}

	row, err := s.policies.FindByID(ctx, id)
	if err != nil {
		return nil, apperr.Internal("could not reload the policy").Wrap(err)
	}
	card := toPolicyCard(*row)
	return &card, nil
}

// ReplaceFileInput is the new document behind PUT /api/v1/policies/:id/file.
type ReplaceFileInput struct {
	FileName string
	MimeType string
	FileSize int64
	File     io.Reader
}

// ReplaceFile swaps a policy's document in place, preserving its id,
// ai_policy_id, and every existing claim correlation — unlike DELETE +
// re-create, which loses all three. Matchmaking is re-queued against the new
// document so correlations stay current, the same way /rematch re-queues it
// after a failure.
func (s *PolicyService) ReplaceFile(ctx context.Context, id uuid.UUID, in ReplaceFileInput) (*dto.PolicyCard, error) {
	row, err := s.policies.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, apperr.NotFound("policy not found")
		}
		return nil, apperr.Internal("could not load policy").Wrap(err)
	}
	if row.ProcessingStatus == models.ProcessingInProgress {
		return nil, apperr.Conflict("matchmaking is already running for this policy")
	}

	objectPath := storage.BuildObjectPath(id, in.FileName)
	object, err := s.store.Upload(ctx, objectPath, in.File, in.FileSize, in.MimeType)
	if err != nil {
		return nil, apperr.Internal("could not store the policy document").Wrap(err)
	}

	size := in.FileSize
	if size <= 0 {
		size = object.Size
	}

	updates := map[string]any{
		"file_name":       in.FileName,
		"file_path":       object.Path,
		"file_mime_type":  object.MimeType,
		"file_size_bytes": size,
	}
	if s.ai.Enabled() {
		updates["processing_status"] = models.ProcessingPending
		updates["processing_attempts"] = 0
		updates["processing_error"] = nil
	}

	oldPath := row.FilePath
	if err := s.policies.Update(ctx, id, updates); err != nil {
		// Roll back the upload so a failed write does not orphan a document.
		if delErr := s.store.Delete(context.Background(), object.Path); delErr != nil {
			log.Printf("[policy] orphaned document %s after failed file replace: %v", object.Path, delErr)
		}
		return nil, apperr.Internal("could not update the policy").Wrap(err)
	}

	if oldPath != "" && oldPath != object.Path {
		if delErr := s.store.Delete(context.Background(), oldPath); delErr != nil {
			log.Printf("[policy] could not delete superseded document %s: %v", oldPath, delErr)
		}
	}

	if s.ai.Enabled() {
		// The document behind the correlations just changed, so the AI service
		// must read the new one rather than re-report matches derived from the
		// superseded file.
		s.startMatchmaking(id, true)
	}

	updated, err := s.policies.FindByID(ctx, id)
	if err != nil {
		return nil, apperr.Internal("could not reload the policy").Wrap(err)
	}
	card := toPolicyCard(*updated)
	return &card, nil
}

// Delete removes a policy and its stored document.
func (s *PolicyService) Delete(ctx context.Context, id uuid.UUID) error {
	row, err := s.policies.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return apperr.NotFound("policy not found")
		}
		return apperr.Internal("could not load policy").Wrap(err)
	}

	if _, err := s.policies.Delete(ctx, id); err != nil {
		return apperr.Internal("could not delete the policy").Wrap(err)
	}

	// Delete the document after the row, so a storage failure leaves a
	// recoverable orphan rather than a record pointing at nothing.
	if row.FilePath != "" {
		if err := s.store.Delete(ctx, row.FilePath); err != nil {
			log.Printf("[policy] deleted policy %s but could not remove document %s: %v", id, row.FilePath, err)
		}
	}
	return nil
}

// Download resolves how to fetch a policy document (US37). It prefers a signed
// URL and falls back to streaming when the driver cannot sign.
func (s *PolicyService) Download(ctx context.Context, id uuid.UUID) (*dto.DownloadResponse, io.ReadCloser, error) {
	row, err := s.policies.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, nil, apperr.NotFound("policy not found")
		}
		return nil, nil, apperr.Internal("could not load policy").Wrap(err)
	}
	if row.FilePath == "" {
		return nil, nil, apperr.NotFound("this policy has no attached document")
	}

	meta := &dto.DownloadResponse{
		FileName:  row.FileName,
		MimeType:  row.FileMimeType,
		SizeBytes: row.FileSizeBytes,
	}

	signed, ok, err := s.store.SignedURL(ctx, row.FilePath)
	if err != nil {
		return nil, nil, apperr.Internal("could not prepare the download").Wrap(err)
	}
	if ok {
		expiry := time.Now().UTC().Add(time.Hour)
		meta.URL = signed
		meta.IsSignedURL = true
		meta.ExpiresAt = &expiry
		return meta, nil, nil
	}

	body, err := s.store.Download(ctx, row.FilePath)
	if err != nil {
		return nil, nil, apperr.Internal("could not read the policy document").Wrap(err)
	}
	return meta, body, nil
}

// RefreshRolloutStatuses flips policies whose rolled-out date has arrived
// (US41). Invoked by the daily cron job and exposed for manual triggering.
func (s *PolicyService) RefreshRolloutStatuses(ctx context.Context) (int64, error) {
	due, err := s.policies.FindDueForRollout(ctx, time.Now().UTC())
	if err != nil {
		return 0, apperr.Internal("could not find policies due for rollout").Wrap(err)
	}
	if len(due) == 0 {
		return 0, nil
	}

	ids := make([]uuid.UUID, 0, len(due))
	for _, p := range due {
		ids = append(ids, p.ID)
	}

	updated, err := s.policies.MarkRolledOut(ctx, ids)
	if err != nil {
		return 0, apperr.Internal("could not update policy statuses").Wrap(err)
	}
	return updated, nil
}

// RetryStuckMatchmaking re-queues policies whose matchmaking never completed.
//
// Two failure shapes are picked up. A job that failed outright leaves
// processing_status="failed" and is obviously retryable. The harder one is a
// lost Flow 2 callback: the AI service acked, the backend recorded
// "processing", and the result never arrived — the AI service's callback is
// best-effort and it never retries. Without a staleness sweep such a policy
// sits at "processing" with a null ai_policy_id forever, spinning its badge and
// showing empty claim lists. So anything that has been "processing" longer than
// AI_MATCHMAKING_STALE_AFTER is re-queued too.
//
// runMatchmaking bumps processing_attempts the moment it starts, which both
// bounds this loop through maxMatchmakingAttempts and stops the next sweep from
// picking up a policy this one has already re-queued.
//
// force is deliberately not set: a run that genuinely succeeded and only lost
// its callback should be cheap for the AI service to re-report.
func (s *PolicyService) RetryStuckMatchmaking(ctx context.Context) (int, error) {
	if !s.ai.Enabled() {
		return 0, nil
	}

	staleBefore := time.Now().UTC().Add(-s.ai.MatchmakingStaleAfter())
	stuck, err := s.policies.FindPendingMatchmaking(ctx, maxMatchmakingAttempts, 20, staleBefore)
	if err != nil {
		return 0, apperr.Internal("could not find pending matchmaking jobs").Wrap(err)
	}
	for _, policy := range stuck {
		s.startMatchmaking(policy.ID, false)
	}
	return len(stuck), nil
}

// isProcessing reports whether the F2 "Processing" badge should be shown.
func isProcessing(status string) bool {
	return status == models.ProcessingPending || status == models.ProcessingInProgress
}

func toPolicyCard(row repository.PolicyRow) dto.PolicyCard {
	card := dto.PolicyCard{
		ID:                  row.ID.String(),
		Name:                row.Name,
		MonthYear:           row.RolledOutDate.Format("January 2006"),
		RolledOutDate:       row.RolledOutDate,
		CreatedAt:           row.CreatedAt,
		Status:              row.Status,
		FileName:            row.FileName,
		FileMimeType:        row.FileMimeType,
		FileSize:            row.FileSizeBytes,
		DownloadURL:         fmt.Sprintf("/api/v1/policies/%s/file", row.ID),
		ProcessingStatus:    row.ProcessingStatus,
		ProcessingError:     row.ProcessingError,
		IsProcessing:        isProcessing(row.ProcessingStatus),
		LinkedClaimCount:    row.LinkedClaimCount,
		LastClaimActivityAt: row.LastClaimActivityAt,
	}
	if row.AIPolicyID != nil {
		id := row.AIPolicyID.String()
		card.AIPolicyID = &id
	}
	return card
}
