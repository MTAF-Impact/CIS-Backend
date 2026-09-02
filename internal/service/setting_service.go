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
	// cache holds a short-lived snapshot of cis_settings. Scoring weights and
	// the CSI parameters are read several times per rendered page and change a
	// handful of times a year, so reading them per use would be a round trip
	// per parameter per request. See dynamic_config.go.
	cache configCache
}

// NewSettingService constructs a SettingService.
//
// cacheTTL is APP's SettingsCacheTTL; a non-positive value falls back to
// defaultConfigCacheTTL rather than disabling the cache, since "no caching"
// would put a query behind every weight read on every rendered page.
func NewSettingService(settings *repository.SettingRepository, cacheTTL time.Duration) *SettingService {
	return &SettingService{settings: settings, cache: configCache{ttl: cacheTTL}}
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
//
// Read through the configuration cache rather than with a query of its own:
// this is the single most-read setting in the system — every claim list, the
// whole Overview page and every snapshot cycle compare against it.
// A read failure is returned rather than swallowed, unlike the
// presentation-only settings. This value decides which claims are Over
// Threshold, and EvaluateCrossings writes a durable notification for every
// claim whose status appears to have changed — so answering with a documented
// default while the real value is unknown would manufacture crossings nobody's
// configuration implies. values() already falls back to the last successful
// read, so this only fires when the process has never managed one.
func (s *SettingService) AlertThreshold(ctx context.Context) (float64, error) {
	values, err := s.values(ctx)
	if err != nil {
		return 0, err
	}
	return scoring.Clamp(parseConfigFloat(values, models.SettingAlertThreshold)), nil
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

	param, _ := models.FindConfigParam(models.SettingAlertThreshold)
	if err := s.settings.UpsertMany(ctx, []repository.SettingWrite{{
		Key:         models.SettingAlertThreshold,
		Value:       formatFloat(threshold),
		ValueType:   "number",
		Description: param.Description,
	}}, updatedBy); err != nil {
		return nil, apperr.Internal("could not save alert threshold").Wrap(err)
	}
	s.cache.invalidate()

	setting, err := s.settings.Get(ctx, models.SettingAlertThreshold)
	if err != nil {
		return nil, apperr.Internal("could not reload alert threshold").Wrap(err)
	}

	view := &ThresholdView{Threshold: threshold, UpdatedAt: setting.UpdatedAt}
	if setting.UpdatedBy != nil {
		id := setting.UpdatedBy.String()
		view.UpdatedBy = &id
	}
	return view, nil
}

// MonitoredCity returns the single Indonesian city this instance monitors
// (PRD v1.5, US65).
//
// An unset or unrecognised value falls back to the default rather than
// erroring: F6 scoped to the wrong city is a smaller failure than an Overview
// page that will not load, and the stored value can only become unrecognised if
// the catalog in models shrinks under it.
func (s *SettingService) MonitoredCity(ctx context.Context) models.City {
	setting, err := s.settings.Get(ctx, models.SettingMonitoredCity)
	if err == nil {
		if city, ok := models.FindCity(setting.Value); ok {
			return city
		}
	}
	city, _ := models.FindCity(models.DefaultMonitoredCity)
	return city
}

// SetMonitoredCity stores the F6 scope city (US65).
//
// Selecting a city also sets the IANA zone used for city-local timestamps on F5
// reports (PRD 10.8). Those were two independent settings before v1.5, which
// meant an instance could be monitoring Makassar while stamping its reports in
// Jakarta time; US65 gives the city a single source of truth, so the timezone
// follows it.
func (s *SettingService) SetMonitoredCity(ctx context.Context, name string, updatedBy *uuid.UUID) (models.City, error) {
	city, ok := models.FindCity(name)
	if !ok {
		return models.City{}, apperr.Unprocessable(
			"%q is not in the list of configurable Indonesian cities; see GET /api/v1/settings/cities", name)
	}

	if _, err := s.settings.Upsert(
		ctx,
		models.SettingMonitoredCity,
		city.Name,
		"string",
		"The single Indonesian city this instance monitors (PRD v1.5, US65). "+
			"Scopes every city-level metric on the F6 Overview page.",
		updatedBy,
	); err != nil {
		return models.City{}, apperr.Internal("could not save the monitored city").Wrap(err)
	}
	s.cache.invalidate()

	if _, err := s.SetCityTimezone(ctx, city.Timezone, updatedBy); err != nil {
		return models.City{}, err
	}
	return city, nil
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
	s.cache.invalidate()
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
