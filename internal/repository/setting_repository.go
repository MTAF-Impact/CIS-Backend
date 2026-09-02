package repository

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/cis/cis-backend/internal/models"
)

// SettingRepository manages cis_settings, the F4 global configuration store.
type SettingRepository struct {
	db *gorm.DB
}

// NewSettingRepository constructs a SettingRepository.
func NewSettingRepository(db *gorm.DB) *SettingRepository {
	return &SettingRepository{db: db}
}

// List returns every configured setting.
func (r *SettingRepository) List(ctx context.Context) ([]models.CISSetting, error) {
	var settings []models.CISSetting
	err := r.db.WithContext(ctx).Order("key ASC").Find(&settings).Error
	return settings, err
}

// Get loads one setting by key.
func (r *SettingRepository) Get(ctx context.Context, key string) (*models.CISSetting, error) {
	var setting models.CISSetting
	err := r.db.WithContext(ctx).Where("key = ?", key).First(&setting).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &setting, nil
}

// Upsert writes a setting, creating it when absent.
//
// The threshold applies globally (US32), so there is exactly one row per key
// and the ON CONFLICT keeps that invariant even under concurrent writes.
func (r *SettingRepository) Upsert(ctx context.Context, key, value, valueType, description string, updatedBy *uuid.UUID) (*models.CISSetting, error) {
	now := time.Now().UTC()
	setting := models.CISSetting{
		ID:          uuid.New(),
		Key:         key,
		Value:       value,
		ValueType:   valueType,
		Description: description,
		UpdatedBy:   updatedBy,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	err := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "key"}},
		DoUpdates: clause.Assignments(map[string]any{
			"value":      value,
			"updated_by": updatedBy,
			"updated_at": now,
		}),
	}).Create(&setting).Error
	if err != nil {
		return nil, err
	}

	return r.Get(ctx, key)
}

// SettingWrite is one key/value change in a batch update.
type SettingWrite struct {
	Key         string
	Value       string
	ValueType   string
	Description string
}

// UpsertMany writes several settings and their history entries in one
// transaction (F4's dynamic parameters; see models.ConfigParams).
//
// # Why this is a batch rather than a loop of Upsert calls
//
// The parameters that travel together are the ones that constrain each other:
// the five composite weights have to sum to 1.00, so a save that persisted
// three of them and then failed would leave the scoring model in a state the
// validator would have rejected outright — every claim in the system scored
// against weights that do not add up, with no error anywhere to say so. One
// transaction makes that unreachable.
//
// Only values that actually moved are written to the history, on the same
// reasoning as SaveDetectorSettings: a save that changes nothing should leave
// no trace, so the log answers "who changed this" rather than "who opened the
// form".
func (r *SettingRepository) UpsertMany(ctx context.Context, writes []SettingWrite, updatedBy *uuid.UUID) error {
	if len(writes) == 0 {
		return nil
	}
	now := time.Now().UTC()

	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing []models.CISSetting
		keys := make([]string, 0, len(writes))
		for _, w := range writes {
			keys = append(keys, w.Key)
		}
		if err := tx.Where("key IN ?", keys).Find(&existing).Error; err != nil {
			return err
		}
		before := make(map[string]string, len(existing))
		for _, row := range existing {
			before[row.Key] = row.Value
		}

		var history []models.CISSettingHistory
		for _, w := range writes {
			setting := models.CISSetting{
				ID:          uuid.New(),
				Key:         w.Key,
				Value:       w.Value,
				ValueType:   w.ValueType,
				Description: w.Description,
				UpdatedBy:   updatedBy,
				CreatedAt:   now,
				UpdatedAt:   now,
			}
			err := tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "key"}},
				DoUpdates: clause.Assignments(map[string]any{
					"value":       w.Value,
					"value_type":  w.ValueType,
					"description": w.Description,
					"updated_by":  updatedBy,
					"updated_at":  now,
				}),
			}).Create(&setting).Error
			if err != nil {
				return err
			}

			prior, had := before[w.Key]
			if had && prior == w.Value {
				continue
			}
			var from *string
			if had {
				v := prior
				from = &v
			}
			history = append(history, models.CISSettingHistory{
				ID:        uuid.New(),
				Key:       w.Key,
				FromValue: from,
				ToValue:   w.Value,
				ChangedBy: updatedBy,
				CreatedAt: now,
			})
		}

		if len(history) == 0 {
			return nil
		}
		return tx.Create(&history).Error
	})
}

// Delete removes a setting, so its value falls back to the documented default
// rather than to a second stored copy of the same number.
func (r *SettingRepository) Delete(ctx context.Context, key string) error {
	return r.db.WithContext(ctx).Where("key = ?", key).Delete(&models.CISSetting{}).Error
}
