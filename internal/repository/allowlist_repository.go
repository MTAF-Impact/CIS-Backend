package repository

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/cis/cis-backend/internal/models"
)

// AllowlistRepository manages the two exclusion lists the detector reads before
// it does anything else: the declared-coordination account allowlist (US56,
// US63) and the common-phrase allowlist (PRD 10.5.2.2).
//
// Both are backend-owned and pipeline-read — the one place the read direction
// between the two services reverses. Everything else the AI service writes and
// this backend reads.
type AllowlistRepository struct {
	db *gorm.DB
}

// NewAllowlistRepository constructs an AllowlistRepository.
func NewAllowlistRepository(db *gorm.DB) *AllowlistRepository {
	return &AllowlistRepository{db: db}
}

// AllowlistFilter narrows the US63 management screen.
type AllowlistFilter struct {
	Search   string
	Platform string
	Category string
	// IncludeRemoved surfaces soft-deleted entries. Off by default: the screen
	// manages active protections, but the removed rows have to remain
	// reachable, because "who removed this NGO's protection, when, and why" is
	// the question the soft delete exists to answer.
	IncludeRemoved bool
	Limit          int
	Offset         int
}

// List returns a page of allowlist entries (US63).
func (r *AllowlistRepository) List(ctx context.Context, f AllowlistFilter) ([]models.CISCoordinationAllowlist, int64, error) {
	q := r.db.WithContext(ctx).Model(&models.CISCoordinationAllowlist{})

	if !f.IncludeRemoved {
		q = q.Where("removed_at IS NULL")
	}
	if f.Platform != "" {
		q = q.Where("platform = ?", f.Platform)
	}
	if f.Category != "" {
		q = q.Where("category = ?", f.Category)
	}
	if s := strings.TrimSpace(f.Search); s != "" {
		pattern := "%" + escapeLike(s) + "%"
		q = q.Where("handle ILIKE ? OR platform_account_id ILIKE ? OR reason ILIKE ?", pattern, pattern, pattern)
	}

	var total int64
	if err := q.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var rows []models.CISCoordinationAllowlist
	err := q.Session(&gorm.Session{}).
		Order("removed_at IS NOT NULL ASC, added_at DESC, id DESC").
		Limit(f.Limit).
		Offset(f.Offset).
		Find(&rows).Error
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// FindByID loads one allowlist entry.
func (r *AllowlistRepository) FindByID(ctx context.Context, id uuid.UUID) (*models.CISCoordinationAllowlist, error) {
	var row models.CISCoordinationAllowlist
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// AllowlistEntryInput is one account being protected.
type AllowlistEntryInput struct {
	Platform          string
	PlatformAccountID string
	Handle            string
	Category          string
	Reason            string
	AddedBy           *uuid.UUID
}

// Add inserts or revives entries, returning how many are newly protected.
//
// # Why upsert rather than insert
//
// US56 adds accounts from two directions — one at a time from the account
// drawer, and a whole network at once from the detail page — and US63 adds them
// manually. The same NGO reached from two networks must not produce a duplicate
// row, and re-adding an account whose protection was previously removed has to
// REVIVE it rather than fail on the unique index. Reviving clears the removal
// fields so the entry does not read as simultaneously active and removed.
//
// The addition reason is overwritten on revival because it is the current
// justification; the previous removal reason is the thing being superseded.
func (r *AllowlistRepository) Add(ctx context.Context, entries []AllowlistEntryInput) (int, error) {
	if len(entries) == 0 {
		return 0, nil
	}

	now := time.Now().UTC()
	rows := make([]models.CISCoordinationAllowlist, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, models.CISCoordinationAllowlist{
			ID:                uuid.New(),
			Platform:          e.Platform,
			PlatformAccountID: e.PlatformAccountID,
			Handle:            e.Handle,
			Category:          e.Category,
			Reason:            e.Reason,
			AddedBy:           e.AddedBy,
			AddedAt:           now,
			CreatedAt:         now,
			UpdatedAt:         now,
		})
	}

	res := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "platform"}, {Name: "platform_account_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"handle":         gorm.Expr("EXCLUDED.handle"),
			"category":       gorm.Expr("EXCLUDED.category"),
			"reason":         gorm.Expr("EXCLUDED.reason"),
			"added_by":       gorm.Expr("EXCLUDED.added_by"),
			"added_at":       now,
			"removed_at":     nil,
			"removed_by":     nil,
			"removal_reason": nil,
			"updated_at":     now,
		}),
	}).Create(&rows)
	if res.Error != nil {
		return 0, res.Error
	}
	return int(res.RowsAffected), nil
}

// Remove soft-deletes an entry with a mandatory reason (US63).
//
// A hard delete would erase who protected an organisation and why, which is
// exactly the record "removal requires a reason and is logged" exists to keep.
func (r *AllowlistRepository) Remove(ctx context.Context, id uuid.UUID, reason string, removedBy *uuid.UUID) error {
	now := time.Now().UTC()
	res := r.db.WithContext(ctx).
		Model(&models.CISCoordinationAllowlist{}).
		Where("id = ? AND removed_at IS NULL", id).
		Updates(map[string]any{
			"removed_at":     now,
			"removed_by":     removedBy,
			"removal_reason": reason,
			"updated_at":     now,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// Update edits an active entry's category or reason (US63).
func (r *AllowlistRepository) Update(ctx context.Context, id uuid.UUID, category, reason string, updatedBy *uuid.UUID) error {
	updates := map[string]any{"updated_at": time.Now().UTC()}
	if category != "" {
		updates["category"] = category
	}
	if reason != "" {
		updates["reason"] = reason
	}
	if updatedBy != nil {
		updates["added_by"] = updatedBy
	}

	res := r.db.WithContext(ctx).
		Model(&models.CISCoordinationAllowlist{}).
		Where("id = ? AND removed_at IS NULL", id).
		Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// ActiveKeys returns every currently protected account identity.
//
// This is the list the pipeline consumes before candidate selection
// (PRD 10.5.1), so it is exposed whole rather than paged.
func (r *AllowlistRepository) ActiveKeys(ctx context.Context) ([]PlatformAccountKey, error) {
	var rows []PlatformAccountKey
	err := r.db.WithContext(ctx).
		Table("cis_coordination_allowlist").
		Select("platform, platform_account_id, handle").
		Where("removed_at IS NULL").
		Order("platform, handle").
		Scan(&rows).Error
	return rows, err
}

// CountByCategory returns active entries per category, for the US63 screen's
// summary and for the onboarding check that the list was seeded at all.
func (r *AllowlistRepository) CountByCategory(ctx context.Context) (map[string]int64, error) {
	type row struct {
		Category string `gorm:"column:category"`
		Total    int64  `gorm:"column:total"`
	}
	var rows []row
	err := r.db.WithContext(ctx).
		Table("cis_coordination_allowlist").
		Select("category, COUNT(*) AS total").
		Where("removed_at IS NULL").
		Group("category").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	out := make(map[string]int64, len(models.ValidAllowlistCategories))
	for _, c := range models.ValidAllowlistCategories {
		out[c] = 0
	}
	for _, r := range rows {
		out[r.Category] = r.Total
	}
	return out, nil
}

// --- Common-phrase allowlist (PRD 10.5.2.2) ---

// ListPhrases returns a page of common phrases.
func (r *AllowlistRepository) ListPhrases(ctx context.Context, search string, limit, offset int) ([]models.CISCommonPhrase, int64, error) {
	q := r.db.WithContext(ctx).Model(&models.CISCommonPhrase{})
	if s := strings.TrimSpace(search); s != "" {
		q = q.Where("phrase ILIKE ?", "%"+escapeLike(s)+"%")
	}

	var total int64
	if err := q.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var rows []models.CISCommonPhrase
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

// AddPhrase inserts a common phrase, ignoring an exact repeat.
func (r *AllowlistRepository) AddPhrase(ctx context.Context, phrase, category string, notes *string, addedBy *uuid.UUID) (*models.CISCommonPhrase, error) {
	now := time.Now().UTC()
	row := models.CISCommonPhrase{
		ID:               uuid.New(),
		Phrase:           phrase,
		NormalizedPhrase: models.NormalizePhrase(phrase),
		Category:         category,
		Notes:            notes,
		AddedBy:          addedBy,
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	err := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "normalized_phrase"}},
		DoNothing: true,
	}).Create(&row).Error
	if err != nil {
		return nil, err
	}

	var stored models.CISCommonPhrase
	if err := r.db.WithContext(ctx).
		Where("normalized_phrase = ?", row.NormalizedPhrase).
		First(&stored).Error; err != nil {
		return nil, err
	}
	return &stored, nil
}

// DeletePhrase removes a common phrase.
//
// Hard delete, unlike the account allowlist. The asymmetry is deliberate: an
// account entry records a decision about a real organisation and who made it,
// while a phrase entry is a tuning knob on a text filter. Nothing is owed to a
// deleted slogan.
func (r *AllowlistRepository) DeletePhrase(ctx context.Context, id uuid.UUID) error {
	res := r.db.WithContext(ctx).Where("id = ?", id).Delete(&models.CISCommonPhrase{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// AllActivePhrases returns every normalised phrase, for the pipeline.
func (r *AllowlistRepository) AllActivePhrases(ctx context.Context) ([]string, error) {
	var out []string
	err := r.db.WithContext(ctx).
		Table("cis_common_phrases").
		Select("normalized_phrase").
		Order("normalized_phrase").
		Scan(&out).Error
	return out, err
}
