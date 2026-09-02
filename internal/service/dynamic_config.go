package service

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/cis/cis-backend/internal/dto"
	"github.com/cis/cis-backend/internal/models"
	"github.com/cis/cis-backend/internal/pkg/apperr"
	"github.com/cis/cis-backend/internal/repository"
	"github.com/cis/cis-backend/internal/scoring"
)

// F4's dynamic-parameter surface: reading and writing the runtime configuration
// held in cis_settings (models.ConfigParams).
//
// These are SettingService methods for the same reason the detector settings
// are: they are one feature — F4's global configuration — and splitting them
// would give one screen two services to talk to.

// defaultConfigCacheTTL bounds how stale a read may be when SETTINGS_CACHE_TTL
// is unset.
//
// Every request that renders a claim score reads five weights, and every
// Overview render reads a dozen more; without a cache that is a round trip per
// parameter per request against a value that changes a handful of times a year.
// The window is short because the only thing it delays is another process's
// write becoming visible — this process's own writes invalidate the cache
// synchronously, so an admin never sees a stale form after saving.
const defaultConfigCacheTTL = 30 * time.Second

// configCache holds one snapshot of every cis_settings row.
//
// The snapshot is kept even after it goes stale, so a failed refresh can fall
// back on real configuration rather than on defaults. Invalidation clears it
// outright, because after our own write the old snapshot is known-wrong rather
// than merely old.
type configCache struct {
	mu       sync.RWMutex
	ttl      time.Duration
	values   map[string]string
	loadedAt time.Time
}

// snapshot returns the cached values and whether they are still fresh. A stale
// snapshot is still returned — the caller decides whether stale beats nothing.
func (c *configCache) snapshot() (map[string]string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.values == nil {
		return nil, false
	}
	ttl := c.ttl
	if ttl <= 0 {
		ttl = defaultConfigCacheTTL
	}
	return c.values, time.Since(c.loadedAt) <= ttl
}

func (c *configCache) store(values map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.values = values
	c.loadedAt = time.Now()
}

func (c *configCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.values = nil
}

// values returns every stored setting as a key/value map, from cache when it is
// fresh.
//
// # What happens when the read fails
//
// It falls back to the last successful read, however old, and errors only when
// there has never been one. Falling back to the REGISTRY DEFAULTS here would be
// wrong in a way that is hard to see: the alert threshold decides which claims
// are Over Threshold, and EvaluateCrossings writes a durable notification for
// every claim whose status appears to have changed. An operator running a
// threshold of 85 whose settings query times out once would have every claim
// scoring between 70 and 85 recorded as having crossed, on the strength of a
// number nobody chose. Stale-but-real cannot do that; a default can.
func (s *SettingService) values(ctx context.Context) (map[string]string, error) {
	cached, fresh := s.cache.snapshot()
	if fresh {
		return cached, nil
	}

	rows, err := s.settings.List(ctx)
	if err != nil {
		if cached != nil {
			return cached, nil
		}
		return map[string]string{}, apperr.Internal("could not load configuration").Wrap(err)
	}

	values := make(map[string]string, len(rows))
	for _, row := range rows {
		values[row.Key] = row.Value
	}
	s.cache.store(values)
	return values, nil
}

// presented returns the stored settings for a read that only displays them, so
// a transient database problem degrades the page to its documented defaults
// instead of failing it.
//
// Never use it for a value that drives a write. Those go through values and
// surface the error — see AlertThreshold.
func (s *SettingService) presented(ctx context.Context) map[string]string {
	values, _ := s.values(ctx)
	return values
}

// resolved returns every registry parameter's current value, stored or default.
func (s *SettingService) resolved(ctx context.Context) map[string]string {
	stored := s.presented(ctx)
	out := make(map[string]string, len(models.ConfigParams))
	for _, p := range models.ConfigParams {
		if v, ok := stored[p.Key]; ok && strings.TrimSpace(v) != "" {
			out[p.Key] = v
			continue
		}
		out[p.Key] = p.Default
	}
	return out
}

// configFloat reads a numeric parameter, falling back to its registry default
// when the row is absent or unparseable.
func (s *SettingService) configFloat(ctx context.Context, key string) float64 {
	return parseConfigFloat(s.presented(ctx), key)
}

// configInt reads an integer parameter, falling back the same way.
func (s *SettingService) configInt(ctx context.Context, key string) int {
	return int(parseConfigFloat(s.presented(ctx), key))
}

func parseConfigFloat(values map[string]string, key string) float64 {
	if raw, ok := values[key]; ok {
		if v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64); err == nil {
			return v
		}
	}
	param, known := models.FindConfigParam(key)
	if !known {
		return 0
	}
	return param.DefaultFloat()
}

// CompositeWeights returns the live PRD 6.3 weights (AP-01..AP-05).
func (s *SettingService) CompositeWeights(ctx context.Context) scoring.Weights {
	values := s.presented(ctx)
	return scoring.Weights{
		Reach:              parseConfigFloat(values, models.SettingWeightReach),
		Velocity:           parseConfigFloat(values, models.SettingWeightVelocity),
		Falseness:          parseConfigFloat(values, models.SettingWeightFalseness),
		Harm:               parseConfigFloat(values, models.SettingWeightHarm),
		EmotionalIntensity: parseConfigFloat(values, models.SettingWeightEmotionalIntensity),
	}
}

// HarmWeights returns the live PRD 6.2.4 sub-weights (AP-06..AP-09).
func (s *SettingService) HarmWeights(ctx context.Context) scoring.HarmWeights {
	values := s.presented(ctx)
	return scoring.HarmWeights{
		PublicSafety:       parseConfigFloat(values, models.SettingHarmWeightPublicSafety),
		InstitutionalTrust: parseConfigFloat(values, models.SettingHarmWeightInstitutionalTrust),
		Economic:           parseConfigFloat(values, models.SettingHarmWeightEconomic),
		PolicyDisruption:   parseConfigFloat(values, models.SettingHarmWeightPolicyDisruption),
	}
}

// DiscountGamma returns the pushback dampening cap (AP-15).
func (s *SettingService) DiscountGamma(ctx context.Context) float64 {
	return s.configFloat(ctx, models.SettingDiscountGamma)
}

// CSIParams returns the configured shape of the Climate Sentiment Index
// (AP-18..AP-20).
//
// RiskThreshold is read from the alert threshold rather than from a row of its
// own: AP-20 is a derived value that always mirrors AP-16, so that "elevated
// risk" cannot come to mean one thing on the Alert page and another on the
// Overview gauge.
func (s *SettingService) CSIParams(ctx context.Context) (scoring.CSIParams, error) {
	threshold, err := s.AlertThreshold(ctx)
	if err != nil {
		return scoring.CSIParams{}, err
	}

	values := s.presented(ctx)
	return scoring.CSIParams{
		WeightBCS:        parseConfigFloat(values, models.SettingCSIWeightBCS),
		WeightRiskLoad:   parseConfigFloat(values, models.SettingCSIWeightRiskLoad),
		RiskThreshold:    threshold,
		MinimumVolume:    int64(parseConfigFloat(values, models.SettingCSIMinimumVolume)),
		WindowDays:       int(parseConfigFloat(values, models.SettingCSIWindowDays)),
		MomentumLagHours: int(parseConfigFloat(values, models.SettingCSIMomentumLagHours)),
		BandRiskyCeiling: parseConfigFloat(values, models.SettingCSIBandRiskyCeiling),
		BandWatchCeiling: parseConfigFloat(values, models.SettingCSIBandWatchCeiling),
	}, nil
}

// OverviewRanking is the configured O2/O3 box-size and leaderboard formula
// (PRD US69, US70).
type OverviewRanking struct {
	WeightAboveCount float64
	WeightAvgScore   float64
	TopPolicyLimit   int
	MoMWindowDays    int
}

// OverviewRanking returns the live Overview ranking configuration.
func (s *SettingService) OverviewRanking(ctx context.Context) OverviewRanking {
	values := s.presented(ctx)
	return OverviewRanking{
		WeightAboveCount: parseConfigFloat(values, models.SettingTreemapWeightAboveCount),
		WeightAvgScore:   parseConfigFloat(values, models.SettingTreemapWeightAvgScore),
		TopPolicyLimit:   int(parseConfigFloat(values, models.SettingTopPolicyLimit)),
		MoMWindowDays:    int(parseConfigFloat(values, models.SettingMoMWindowDays)),
	}
}

// ScoreSnapshotRetention is how long per-claim score history is kept before the
// hourly job prunes it (US27).
func (s *SettingService) ScoreSnapshotRetention(ctx context.Context) time.Duration {
	return time.Duration(s.configInt(ctx, models.SettingScoreSnapshotRetentionDays)) * 24 * time.Hour
}

// ConfigCatalog is the whole F4 dynamic-parameter surface in one payload: the
// registry's metadata, the current values, and the sections to render them in.
//
// Served rather than duplicated in the frontend for the same reason the
// detector ranges are: two copies of a bound drift, and the drift shows up as a
// form that accepts a value the server then rejects.
func (s *SettingService) ConfigCatalog(ctx context.Context) *dto.ConfigCatalog {
	stored := s.presented(ctx)
	threshold := parseConfigFloat(stored, models.SettingAlertThreshold)

	sections := make([]dto.ConfigSectionView, 0, len(models.ConfigSections))
	for _, section := range models.ConfigSections {
		view := dto.ConfigSectionView{
			Key:         section.Key,
			Tier:        section.Tier,
			Title:       section.Title,
			Description: section.Description,
		}
		for _, p := range models.ConfigParams {
			if p.Section != section.Key {
				continue
			}
			view.Parameters = append(view.Parameters, s.configParamView(p, stored, threshold))
		}
		if len(view.Parameters) > 0 {
			sections = append(sections, view)
		}
	}

	return &dto.ConfigCatalog{
		Tiers: []dto.ConfigTier{
			{
				Key:   models.ConfigTierOperations,
				Title: "Operational settings",
				Description: "Day-to-day controls a city administrator can change safely. Each one changes " +
					"what the product shows or how much it produces, never how a score is computed.",
			},
			{
				Key:   models.ConfigTierAnalytics,
				Title: "Model & analytics settings",
				Description: "Values that change what a score means. Editable by an admin, but the decision " +
					"belongs with the engineering and data team — every one of them moves every claim's rank.",
			},
		},
		Sections:    sections,
		GeneratedAt: time.Now().UTC(),
	}
}

func (s *SettingService) configParamView(
	p models.ConfigParam, stored map[string]string, alertThreshold float64,
) dto.ConfigParamView {
	view := dto.ConfigParamView{
		ConfigParam: p,
		Writable:    p.Writable(),
		Value:       p.Default,
	}

	if p.Derived {
		// The only derived parameter is the CSI risk threshold, which mirrors
		// the alert threshold (AP-20).
		view.Value = formatFloat(alertThreshold)
		return view
	}
	if v, ok := stored[p.Key]; ok && strings.TrimSpace(v) != "" {
		view.Value = v
	}
	// IsSet asks "has anyone moved this off its documented default", not "does a
	// row exist" — the seed writes a row for every parameter, so row existence
	// answers nothing. This is what F4 gates its reset control on.
	view.IsSet = view.Value != p.Default
	return view
}

// UpdateConfigParams applies a partial update to the dynamic parameters.
//
// # Why the whole set is validated, not just the changed keys
//
// Four of these parameters belong to sets that must total 1.00, and one — the
// harm Policy Disruption weight — carries a ceiling that exists to stop the
// tool being tuned into one that scores criticism of a government as harm.
// Saving one member of a sum group is only meaningful in terms of the others,
// so the pending changes are merged over the stored values first and the whole
// resolved set is checked. Validating the diff alone would let two legal saves
// leave the weights summing to 0.9 — which lowers every claim's score in the
// system, silently, with nothing on screen to say so.
//
// # Why every change is written in one transaction with its history
//
// US62's rule for the detector applies here for the same reason: a governed
// parameter persisted without its history entry is a parameter changed by
// nobody. A save that changes nothing writes nothing, which is what keeps the
// history readable.
func (s *SettingService) UpdateConfigParams(
	ctx context.Context, changes map[string]string, updatedBy *uuid.UUID,
) (*dto.ConfigCatalog, error) {
	if len(changes) == 0 {
		return nil, apperr.BadRequest("no parameters were provided")
	}

	fieldErrs := map[string]string{}
	clean := make(map[string]string, len(changes))

	for key, raw := range changes {
		param, ok := models.FindConfigParam(key)
		if !ok {
			fieldErrs[key] = "is not a configurable parameter"
			continue
		}
		if !param.Writable() {
			if param.Derived {
				fieldErrs[key] = "is derived from another parameter and cannot be set directly"
			} else {
				fieldErrs[key] = "must be changed through " + param.ManagedBy
			}
			continue
		}
		value := strings.TrimSpace(raw)
		if err := param.ValidateValue(value); err != nil {
			fieldErrs[key] = err.Error()
			continue
		}
		clean[key] = value
	}

	if len(fieldErrs) > 0 {
		return nil, apperr.Unprocessable("some parameters failed validation").WithDetails(fieldErrs)
	}

	// Merge over the resolved current state so the cross-field rules see every
	// member of every group, whether or not this request touched it.
	merged := s.resolved(ctx)
	for key, value := range clean {
		merged[key] = value
	}
	if errs := models.ValidateConfigSet(merged); len(errs) > 0 {
		return nil, apperr.Unprocessable("the parameters are individually valid but inconsistent together").
			WithDetails(errs)
	}

	writes := make([]repository.SettingWrite, 0, len(clean))
	for key, value := range clean {
		param, _ := models.FindConfigParam(key)
		writes = append(writes, repository.SettingWrite{
			Key:         key,
			Value:       value,
			ValueType:   param.ValueType(),
			Description: param.Description,
		})
	}
	// Deterministic order keeps the history rows of one save readable, and
	// makes the write path reproducible in tests.
	sort.Slice(writes, func(i, j int) bool { return writes[i].Key < writes[j].Key })

	if err := s.settings.UpsertMany(ctx, writes, updatedBy); err != nil {
		return nil, apperr.Internal("could not save the configuration").Wrap(err)
	}
	s.cache.invalidate()

	return s.ConfigCatalog(ctx), nil
}

// ResetConfigParam restores one parameter to its documented default by deleting
// its row, so the value falls back to the registry rather than to a second
// stored copy of the same number.
func (s *SettingService) ResetConfigParam(ctx context.Context, key string, updatedBy *uuid.UUID) error {
	param, ok := models.FindConfigParam(key)
	if !ok {
		return apperr.NotFound("%q is not a configurable parameter", key)
	}
	if !param.Writable() {
		return apperr.Unprocessable("%q cannot be reset through this endpoint", key)
	}

	var from *string
	if existing, err := s.settings.Get(ctx, key); err == nil {
		v := existing.Value
		from = &v
	} else if !errors.Is(err, repository.ErrNotFound) {
		return apperr.Internal("could not read the current value").Wrap(err)
	}

	if from == nil || *from == param.Default {
		return nil
	}

	if err := s.settings.Delete(ctx, key); err != nil {
		return apperr.Internal("could not reset the parameter").Wrap(err)
	}
	s.cache.invalidate()

	if err := s.settings.RecordSettingChange(ctx, key, from, param.Default, updatedBy); err != nil {
		return apperr.Internal("could not record the reset").Wrap(err)
	}
	return nil
}
