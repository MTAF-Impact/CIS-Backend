package repository

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/cis/cis-backend/internal/models"
)

// F5 detector configuration and the versioned change log behind it (US62).
//
// These hang off SettingRepository rather than a new type because they are the
// same feature as the alert threshold: F4's governed global configuration. The
// only reason the detector parameters get a typed table of their own is that
// two of their constraints are cross-field, which a key/value setter cannot
// check. See models.CISDetectorSettings.

// DetectorSettings loads the single cis_detector_settings row.
//
// A missing row is not an error: it means the seed has not run on this database
// yet, and PRD 10.11's defaults are the right answer either way. Returning them
// keeps the F5 config screen and the scheduler working on a fresh deployment
// rather than failing on a bootstrapping detail.
func (r *SettingRepository) DetectorSettings(ctx context.Context) (*models.CISDetectorSettings, error) {
	var s models.CISDetectorSettings
	err := r.db.WithContext(ctx).Where("id = ?", models.DetectorSettingsID).First(&s).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		defaults := models.DefaultDetectorSettings()
		return &defaults, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// SaveDetectorSettings writes the whole parameter set and records every changed
// field in cis_setting_history, in one transaction.
//
// # Why the history is written field by field
//
// US62 requires "all changes are versioned with user and timestamp". One row
// per SAVE would answer "who touched the config" but not "who changed
// theta_edge", and the second question is the one asked when a run's results
// look wrong six weeks later. So the stored row is diffed against the incoming
// one and only fields that actually moved are logged — a save that changes
// nothing writes nothing, which is what keeps the history readable.
//
// The transaction matters for the same reason the review overlay and its log
// share one: a governed parameter persisted without its history entry is a
// parameter changed by nobody.
func (r *SettingRepository) SaveDetectorSettings(
	ctx context.Context, next models.CISDetectorSettings, updatedBy *uuid.UUID,
) (*models.CISDetectorSettings, error) {
	now := time.Now().UTC()
	next.ID = models.DetectorSettingsID
	next.UpdatedBy = updatedBy
	next.UpdatedAt = now

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current models.CISDetectorSettings
		err := tx.Where("id = ?", models.DetectorSettingsID).First(&current).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			current = models.DefaultDetectorSettings()
			next.CreatedAt = now
		case err != nil:
			return err
		default:
			next.CreatedAt = current.CreatedAt
		}

		if err := tx.Save(&next).Error; err != nil {
			return err
		}

		changes := diffDetectorSettings(current, next)
		if len(changes) == 0 {
			return nil
		}

		entries := make([]models.CISSettingHistory, 0, len(changes))
		for _, c := range changes {
			from := c.From
			entries = append(entries, models.CISSettingHistory{
				ID:        uuid.New(),
				Key:       DetectorSettingPrefix + c.Key,
				FromValue: &from,
				ToValue:   c.To,
				ChangedBy: updatedBy,
				CreatedAt: now,
			})
		}
		return tx.Create(&entries).Error
	})
	if err != nil {
		return nil, err
	}
	return &next, nil
}

// DetectorSettingPrefix namespaces detector parameters in the shared history
// table, so "show me every detector change" is one LIKE rather than a list of
// thirty keys that has to be updated whenever a parameter is added.
const DetectorSettingPrefix = "detector."

// settingChange is one field that moved between two versions of the config.
type settingChange struct {
	Key  string
	From string
	To   string
}

// diffDetectorSettings compares two parameter sets field by field.
//
// Floats are compared through their formatted form rather than with an epsilon:
// every value here is an operator-entered decimal, so a difference that survives
// formatting is a difference an operator made, and one that does not is not
// worth a history row.
func diffDetectorSettings(before, after models.CISDetectorSettings) []settingChange {
	var out []settingChange

	addInt := func(key string, a, b int) {
		if a != b {
			out = append(out, settingChange{Key: key, From: strconv.Itoa(a), To: strconv.Itoa(b)})
		}
	}
	addFloat := func(key string, a, b float64) {
		fa := strconv.FormatFloat(a, 'g', -1, 64)
		fb := strconv.FormatFloat(b, 'g', -1, 64)
		if fa != fb {
			out = append(out, settingChange{Key: key, From: fa, To: fb})
		}
	}
	addBool := func(key string, a, b bool) {
		if a != b {
			out = append(out, settingChange{Key: key, From: strconv.FormatBool(a), To: strconv.FormatBool(b)})
		}
	}

	addInt("window_days", before.WindowDays, after.WindowDays)
	addInt("bin_width_seconds", before.BinWidthSeconds, after.BinWidthSeconds)
	addFloat("null_model_alpha", before.NullModelAlpha, after.NullModelAlpha)
	addFloat("dup_threshold", before.DupThreshold, after.DupThreshold)
	addFloat("sem_threshold", before.SemThreshold, after.SemThreshold)
	addInt("min_post_length", before.MinPostLength, after.MinPostLength)
	addFloat("edge_threshold", before.EdgeThreshold, after.EdgeThreshold)
	addInt("min_signal_families", before.MinSignalFamilies, after.MinSignalFamilies)
	addInt("k_core", before.KCore, after.KCore)
	addFloat("leiden_resolution", before.LeidenResolution, after.LeidenResolution)
	addInt("min_cluster_size", before.MinClusterSize, after.MinClusterSize)
	addFloat("min_internal_density", before.MinInternalDensity, after.MinInternalDensity)
	addFloat("beta_time", before.BetaTime, after.BetaTime)
	addFloat("beta_text", before.BetaText, after.BetaText)
	addFloat("beta_amp", before.BetaAmp, after.BetaAmp)
	addFloat("beta_meta", before.BetaMeta, after.BetaMeta)
	addFloat("beta_struct", before.BetaStruct, after.BetaStruct)
	addInt("provenance_half_life_hours", before.ProvenanceHalfLifeHours, after.ProvenanceHalfLifeHours)
	addFloat("anchor_share", before.AnchorShare, after.AnchorShare)
	addInt("min_claim_posts", before.MinClaimPosts, after.MinClaimPosts)
	addFloat("min_link_strength", before.MinLinkStrength, after.MinLinkStrength)
	addFloat("high_score_cutoff", before.HighScoreCutoff, after.HighScoreCutoff)
	addInt("high_breadth_cutoff", before.HighBreadthCutoff, after.HighBreadthCutoff)
	addFloat("medium_score_cutoff", before.MediumScoreCutoff, after.MediumScoreCutoff)
	addInt("medium_breadth_cutoff", before.MediumBreadthCutoff, after.MediumBreadthCutoff)
	addInt("cadence_hours", before.CadenceHours, after.CadenceHours)
	addInt("candidate_cap", before.CandidateCap, after.CandidateCap)
	addFloat("recurrence_threshold", before.RecurrenceThreshold, after.RecurrenceThreshold)
	addFloat("velocity_trigger_threshold", before.VelocityTriggerThreshold, after.VelocityTriggerThreshold)
	addBool("velocity_trigger_enabled", before.VelocityTriggerEnabled, after.VelocityTriggerEnabled)

	return out
}

// RecordSettingChange appends one flat cis_settings change to the history.
//
// The detector parameters get versioned through SaveDetectorSettings; this
// covers the key/value settings, so the alert threshold (US32) and the city
// timezone are versioned by the same mechanism rather than by none. US62 only
// demands it for the detector, but the threshold has been globally governed
// since v1.3 with no record of who moved it.
func (r *SettingRepository) RecordSettingChange(
	ctx context.Context, key string, from *string, to string, changedBy *uuid.UUID,
) error {
	entry := models.CISSettingHistory{
		ID:        uuid.New(),
		Key:       key,
		FromValue: from,
		ToValue:   to,
		ChangedBy: changedBy,
		CreatedAt: time.Now().UTC(),
	}
	return r.db.WithContext(ctx).Create(&entry).Error
}

// ListSettingHistory returns configuration changes, newest first (US62).
func (r *SettingRepository) ListSettingHistory(
	ctx context.Context, keyPrefix string, limit, offset int,
) ([]models.CISSettingHistory, int64, error) {
	q := r.db.WithContext(ctx).Model(&models.CISSettingHistory{})
	if keyPrefix != "" {
		q = q.Where("key LIKE ?", escapeLike(keyPrefix)+"%")
	}

	var total int64
	if err := q.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var rows []models.CISSettingHistory
	err := q.Session(&gorm.Session{}).
		Order("created_at DESC, id DESC").
		Limit(limit).
		Offset(offset).
		Find(&rows).Error
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}
