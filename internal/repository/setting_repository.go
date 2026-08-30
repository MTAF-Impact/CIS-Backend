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
