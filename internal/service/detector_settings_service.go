package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/cis/cis-backend/internal/dto"
	"github.com/cis/cis-backend/internal/models"
	"github.com/cis/cis-backend/internal/pkg/apperr"
	"github.com/cis/cis-backend/internal/repository"
)

// The coordinated-network detector's admin surface: the governed detector
// configuration.
//
// These are SettingService methods rather than a separate service because they
// are the same feature as the alert threshold — governed global configuration
// — and splitting them would give the settings page two services to talk to
// for one screen.

// DetectorSettings loads the current parameter set.
func (s *SettingService) DetectorSettings(ctx context.Context) (*models.CISDetectorSettings, error) {
	settings, err := s.settings.DetectorSettings(ctx)
	if err != nil {
		return nil, apperr.Internal("could not load detector settings").Wrap(err)
	}
	return settings, nil
}

// DetectorSettingsView returns the parameter set with its audit metadata and
// the self-exclusion count.
func (s *SettingService) DetectorSettingsView(ctx context.Context, selfExclusionCount int64) (*dto.DetectorSettingsView, error) {
	settings, err := s.DetectorSettings(ctx)
	if err != nil {
		return nil, err
	}
	view := toDetectorSettingsView(*settings)
	view.SelfExclusionCount = selfExclusionCount
	return view, nil
}

// DetectorParamRanges exposes the default parameter reference so the settings
// screen can render bounded inputs without duplicating the table.
//
// Served rather than hardcoded in the frontend for the same reason the
// disclaimer is: two copies of a specification drift, and here the drift would
// be a form that accepts a value the server then rejects.
func (s *SettingService) DetectorParamRanges() []models.ParamRange {
	return models.DetectorParamRanges
}

// UpdateDetectorSettings validates and stores a new parameter set.
//
// # Partial updates
//
// Every field in the request is a pointer, and an omitted one keeps its stored
// value. A screen saving one threshold must not silently reset the other
// twenty-nine to whatever its form happened to default to — and because these
// parameters govern what the detector accuses people of, a silent reset is not
// a cosmetic bug.
//
// # Where validation lives
//
// In models.CISDetectorSettings.Validate, not in struct tags, because two of the
// constraints are cross-field: the five fusion weights must sum to 1.00, and the
// scheduled cadence may not exceed half the detection window (which is how
// consecutive runs are kept overlapping by at least 50%). A validator tag
// cannot see a sibling field, and both constraints are reachable using values
// that are individually legal.
func (s *SettingService) UpdateDetectorSettings(
	ctx context.Context, req dto.UpdateDetectorSettingsRequest, updatedBy *uuid.UUID,
) (*dto.DetectorSettingsView, error) {
	current, err := s.DetectorSettings(ctx)
	if err != nil {
		return nil, err
	}

	next := *current
	applyInt(&next.WindowDays, req.WindowDays)
	applyInt(&next.BinWidthSeconds, req.BinWidthSeconds)
	applyFloat(&next.NullModelAlpha, req.NullModelAlpha)
	applyFloat(&next.DupThreshold, req.DupThreshold)
	applyFloat(&next.SemThreshold, req.SemThreshold)
	applyInt(&next.MinPostLength, req.MinPostLength)
	applyFloat(&next.EdgeThreshold, req.EdgeThreshold)
	applyInt(&next.MinSignalFamilies, req.MinSignalFamilies)
	applyInt(&next.KCore, req.KCore)
	applyFloat(&next.LeidenResolution, req.LeidenResolution)
	applyInt(&next.MinClusterSize, req.MinClusterSize)
	applyFloat(&next.MinInternalDensity, req.MinInternalDensity)
	applyFloat(&next.BetaTime, req.BetaTime)
	applyFloat(&next.BetaText, req.BetaText)
	applyFloat(&next.BetaAmp, req.BetaAmp)
	applyFloat(&next.BetaMeta, req.BetaMeta)
	applyFloat(&next.BetaStruct, req.BetaStruct)
	applyInt(&next.ProvenanceHalfLifeHours, req.ProvenanceHalfLifeHours)
	applyFloat(&next.AnchorShare, req.AnchorShare)
	applyInt(&next.MinClaimPosts, req.MinClaimPosts)
	applyFloat(&next.MinLinkStrength, req.MinLinkStrength)
	applyFloat(&next.HighScoreCutoff, req.HighScoreCutoff)
	applyInt(&next.HighBreadthCutoff, req.HighBreadthCutoff)
	applyFloat(&next.MediumScoreCutoff, req.MediumScoreCutoff)
	applyInt(&next.MediumBreadthCutoff, req.MediumBreadthCutoff)
	applyInt(&next.CadenceHours, req.CadenceHours)
	applyInt(&next.CandidateCap, req.CandidateCap)
	applyFloat(&next.RecurrenceThreshold, req.RecurrenceThreshold)
	applyFloat(&next.VelocityTriggerThreshold, req.VelocityTriggerThreshold)
	if req.VelocityTriggerEnabled != nil {
		next.VelocityTriggerEnabled = *req.VelocityTriggerEnabled
	}

	if errs := next.Validate(); len(errs) > 0 {
		return nil, apperr.Unprocessable("detector settings failed validation").WithDetails(errs)
	}

	saved, err := s.settings.SaveDetectorSettings(ctx, next, updatedBy)
	if err != nil {
		return nil, apperr.Internal("could not save detector settings").Wrap(err)
	}
	return toDetectorSettingsView(*saved), nil
}

func applyInt(dst *int, src *int) {
	if src != nil {
		*dst = *src
	}
}

func applyFloat(dst *float64, src *float64) {
	if src != nil {
		*dst = *src
	}
}

func toDetectorSettingsView(s models.CISDetectorSettings) *dto.DetectorSettingsView {
	view := &dto.DetectorSettingsView{
		WindowDays:               s.WindowDays,
		BinWidthSeconds:          s.BinWidthSeconds,
		NullModelAlpha:           s.NullModelAlpha,
		DupThreshold:             s.DupThreshold,
		SemThreshold:             s.SemThreshold,
		MinPostLength:            s.MinPostLength,
		EdgeThreshold:            s.EdgeThreshold,
		MinSignalFamilies:        s.MinSignalFamilies,
		KCore:                    s.KCore,
		LeidenResolution:         s.LeidenResolution,
		MinClusterSize:           s.MinClusterSize,
		MinInternalDensity:       s.MinInternalDensity,
		BetaTime:                 s.BetaTime,
		BetaText:                 s.BetaText,
		BetaAmp:                  s.BetaAmp,
		BetaMeta:                 s.BetaMeta,
		BetaStruct:               s.BetaStruct,
		ProvenanceHalfLifeHours:  s.ProvenanceHalfLifeHours,
		AnchorShare:              s.AnchorShare,
		MinClaimPosts:            s.MinClaimPosts,
		MinLinkStrength:          s.MinLinkStrength,
		HighScoreCutoff:          s.HighScoreCutoff,
		HighBreadthCutoff:        s.HighBreadthCutoff,
		MediumScoreCutoff:        s.MediumScoreCutoff,
		MediumBreadthCutoff:      s.MediumBreadthCutoff,
		CadenceHours:             s.CadenceHours,
		CandidateCap:             s.CandidateCap,
		RecurrenceThreshold:      s.RecurrenceThreshold,
		VelocityTriggerThreshold: s.VelocityTriggerThreshold,
		VelocityTriggerEnabled:   s.VelocityTriggerEnabled,
		UpdatedAt:                s.UpdatedAt,
	}
	if s.UpdatedBy != nil {
		id := s.UpdatedBy.String()
		view.UpdatedBy = &id
	}
	return view
}

// SettingHistory returns configuration changes, newest first.
//
// keyPrefix narrows to one family: repository.DetectorSettingPrefix for the
// detector parameters, or a bare key such as "alert_threshold".
func (s *SettingService) SettingHistory(
	ctx context.Context, keyPrefix string, page, limit int,
) ([]dto.SettingHistoryEntry, int64, dto.PageParams, error) {
	window := dto.NormalizePage(page, limit)

	rows, total, err := s.settings.ListSettingHistory(ctx, keyPrefix, window.Limit, window.Offset())
	if err != nil {
		return nil, 0, window, apperr.Internal("could not load the settings history").Wrap(err)
	}

	out := make([]dto.SettingHistoryEntry, 0, len(rows))
	for _, r := range rows {
		entry := dto.SettingHistoryEntry{
			ID:        r.ID.String(),
			Key:       r.Key,
			FromValue: r.FromValue,
			ToValue:   r.ToValue,
			CreatedAt: r.CreatedAt,
		}
		if r.ChangedBy != nil {
			id := r.ChangedBy.String()
			entry.ChangedBy = &id
		}
		out = append(out, entry)
	}
	return out, total, window, nil
}

// CityTimezone returns the IANA zone used for the city-local half of every
// report footer timestamp.
//
// Every page must show "UTC and city-local time" without naming the city;
// nothing else in the system knows either, and the scheduler is pinned to
// UTC. An unparseable stored value falls back to the default rather than
// failing the report, because a footer in the wrong zone is a smaller failure
// than a report that will not render.
func (s *SettingService) CityTimezone(ctx context.Context) *time.Location {
	setting, err := s.settings.Get(ctx, models.SettingCityTimezone)
	name := models.DefaultCityTimezone
	if err == nil && strings.TrimSpace(setting.Value) != "" {
		name = strings.TrimSpace(setting.Value)
	}

	loc, err := time.LoadLocation(name)
	if err != nil {
		if fallback, err2 := time.LoadLocation(models.DefaultCityTimezone); err2 == nil {
			return fallback
		}
		return time.UTC
	}
	return loc
}

// SetCityTimezone stores the report footer's local zone.
func (s *SettingService) SetCityTimezone(ctx context.Context, name string, updatedBy *uuid.UUID) (string, error) {
	name = strings.TrimSpace(name)
	if _, err := time.LoadLocation(name); err != nil {
		return "", apperr.Unprocessable("%q is not a recognised IANA timezone (for example: Asia/Jakarta)", name)
	}

	var from *string
	if existing, err := s.settings.Get(ctx, models.SettingCityTimezone); err == nil {
		v := existing.Value
		from = &v
	} else if !errors.Is(err, repository.ErrNotFound) {
		return "", apperr.Internal("could not read the current city timezone").Wrap(err)
	}

	if _, err := s.settings.Upsert(
		ctx, models.SettingCityTimezone, name, "string",
		"IANA timezone for the city-local half of every F5 report footer timestamp (PRD 10.8).",
		updatedBy,
	); err != nil {
		return "", apperr.Internal("could not save the city timezone").Wrap(err)
	}

	s.cache.invalidate()

	if err := s.settings.RecordSettingChange(ctx, models.SettingCityTimezone, from, name, updatedBy); err != nil {
		return "", apperr.Internal("could not record the city timezone change").Wrap(err)
	}
	return name, nil
}
