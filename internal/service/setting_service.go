package service

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/cis/cis-backend/internal/models"
	"github.com/cis/cis-backend/internal/pkg/apperr"
	"github.com/cis/cis-backend/internal/repository"
	"github.com/cis/cis-backend/internal/scoring"
)

// SettingService serves the F4 Admin Settings page.
type SettingService struct {
	settings *repository.SettingRepository
}

// NewSettingService constructs a SettingService.
func NewSettingService(settings *repository.SettingRepository) *SettingService {
	return &SettingService{settings: settings}
}

// SettingView is the public representation of a configuration value.
type SettingView struct {
	Key         string    `json:"key"`
	Value       string    `json:"value"`
	ValueType   string    `json:"value_type"`
	Description string    `json:"description"`
	UpdatedAt   time.Time `json:"updated_at"`
	UpdatedBy   *string   `json:"updated_by"`
}

// ThresholdView is the alert threshold payload (US32).
type ThresholdView struct {
	Threshold float64   `json:"threshold"`
	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy *string   `json:"updated_by"`
}

// List returns every global setting.
func (s *SettingService) List(ctx context.Context) ([]SettingView, error) {
	settings, err := s.settings.List(ctx)
	if err != nil {
		return nil, apperr.Internal("could not load settings").Wrap(err)
	}

	out := make([]SettingView, 0, len(settings))
	for _, setting := range settings {
		out = append(out, toSettingView(setting))
	}
	return out, nil
}

// AlertThreshold returns the global Over/Under Threshold cutoff used by F3
// (US29, US32). It falls back to the documented default when the row is
// missing, so the Alert page never breaks on a fresh database.
func (s *SettingService) AlertThreshold(ctx context.Context) (float64, error) {
	setting, err := s.settings.Get(ctx, models.SettingAlertThreshold)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return models.DefaultAlertThreshold, nil
		}
		return 0, apperr.Internal("could not load alert threshold").Wrap(err)
	}

	value, parseErr := strconv.ParseFloat(setting.Value, 64)
	if parseErr != nil {
		return models.DefaultAlertThreshold, nil
	}
	return scoring.Clamp(value), nil
}

// AlertThresholdView returns the threshold with its audit metadata.
func (s *SettingService) AlertThresholdView(ctx context.Context) (*ThresholdView, error) {
	setting, err := s.settings.Get(ctx, models.SettingAlertThreshold)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return &ThresholdView{Threshold: models.DefaultAlertThreshold, UpdatedAt: time.Now().UTC()}, nil
		}
		return nil, apperr.Internal("could not load alert threshold").Wrap(err)
	}

	value, parseErr := strconv.ParseFloat(setting.Value, 64)
	if parseErr != nil {
		value = models.DefaultAlertThreshold
	}

	view := &ThresholdView{Threshold: scoring.Clamp(value), UpdatedAt: setting.UpdatedAt}
	if setting.UpdatedBy != nil {
		id := setting.UpdatedBy.String()
		view.UpdatedBy = &id
	}
	return view, nil
}

// SetAlertThreshold stores a new global threshold (US32).
func (s *SettingService) SetAlertThreshold(ctx context.Context, threshold float64, updatedBy *uuid.UUID) (*ThresholdView, error) {
	// The threshold is compared against FinalClaimScore, which PRD 6.5 fixes to
	// a 0-100 scale, so anything outside that range is meaningless.
	if threshold < scoring.MinScore || threshold > scoring.MaxScore {
		return nil, apperr.Unprocessable("threshold must be between 0 and 100")
	}

	setting, err := s.settings.Upsert(
		ctx,
		models.SettingAlertThreshold,
		formatFloat(threshold),
		"number",
		"Global FinalClaimScore threshold (0-100) deciding Over/Under Threshold on the Alert page (PRD US32).",
		updatedBy,
	)
	if err != nil {
		return nil, apperr.Internal("could not save alert threshold").Wrap(err)
	}

	view := &ThresholdView{Threshold: threshold, UpdatedAt: setting.UpdatedAt}
	if setting.UpdatedBy != nil {
		id := setting.UpdatedBy.String()
		view.UpdatedBy = &id
	}
	return view, nil
}

// ClaimsLastFetchedAt returns the "last fetched" timestamp shown on S1 (US9).
func (s *SettingService) ClaimsLastFetchedAt(ctx context.Context) (time.Time, error) {
	setting, err := s.settings.Get(ctx, models.SettingClaimsLastFetchedAt)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return time.Now().UTC(), nil
		}
		return time.Time{}, apperr.Internal("could not load last fetched timestamp").Wrap(err)
	}

	parsed, parseErr := time.Parse(time.RFC3339, setting.Value)
	if parseErr != nil {
		return setting.UpdatedAt, nil
	}
	return parsed, nil
}

// TouchClaimsLastFetchedAt updates the S1 "last fetched" timestamp. US33
// requires this to move to the moment the Generate Generic Claim button was
// clicked.
func (s *SettingService) TouchClaimsLastFetchedAt(ctx context.Context, at time.Time, updatedBy *uuid.UUID) (time.Time, error) {
	_, err := s.settings.Upsert(
		ctx,
		models.SettingClaimsLastFetchedAt,
		at.UTC().Format(time.RFC3339),
		"timestamp",
		"Timestamp shown as 'last fetched' on the Existing Claim section (PRD US9/US33).",
		updatedBy,
	)
	if err != nil {
		return time.Time{}, apperr.Internal("could not update last fetched timestamp").Wrap(err)
	}
	return at.UTC(), nil
}

func toSettingView(setting models.CISSetting) SettingView {
	view := SettingView{
		Key:         setting.Key,
		Value:       setting.Value,
		ValueType:   setting.ValueType,
		Description: setting.Description,
		UpdatedAt:   setting.UpdatedAt,
	}
	if setting.UpdatedBy != nil {
		id := setting.UpdatedBy.String()
		view.UpdatedBy = &id
	}
	return view
}
