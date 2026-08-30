// Package repository contains every database query in the application.
//
// Only repositories talk to GORM; services and handlers never do. Repositories
// backed by AI-owned tables expose read methods exclusively — see
// models/ai_tables.go for the ownership rules.
package repository

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/cis/cis-backend/internal/models"
)

// ErrNotFound is returned when a lookup matches no row. Services translate it
// into an apperr with a resource-specific message.
var ErrNotFound = errors.New("record not found")

// UserRepository provides access to cis_users and cis_refresh_tokens.
type UserRepository struct {
	db *gorm.DB
}

// NewUserRepository constructs a UserRepository.
func NewUserRepository(db *gorm.DB) *UserRepository {
	return &UserRepository{db: db}
}

// CreateUser inserts a new operator account.
func (r *UserRepository) CreateUser(ctx context.Context, user *models.CISUser) error {
	user.Email = normalizeEmail(user.Email)
	return r.db.WithContext(ctx).Create(user).Error
}

// FindUserByEmail looks up an account by its normalized email.
func (r *UserRepository) FindUserByEmail(ctx context.Context, email string) (*models.CISUser, error) {
	var user models.CISUser
	err := r.db.WithContext(ctx).Where("email = ?", normalizeEmail(email)).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// FindUserByID looks up an account by id.
func (r *UserRepository) FindUserByID(ctx context.Context, id uuid.UUID) (*models.CISUser, error) {
	var user models.CISUser
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// EmailExists reports whether an account already uses the given email.
func (r *UserRepository) EmailExists(ctx context.Context, email string) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&models.CISUser{}).
		Where("email = ?", normalizeEmail(email)).
		Count(&count).Error
	return count > 0, err
}

// TouchLastLogin records a successful authentication.
func (r *UserRepository) TouchLastLogin(ctx context.Context, id uuid.UUID, at time.Time) error {
	return r.db.WithContext(ctx).
		Model(&models.CISUser{}).
		Where("id = ?", id).
		Updates(map[string]any{"last_login_at": at, "updated_at": at}).Error
}

// CreateRefreshToken stores a hashed refresh token.
func (r *UserRepository) CreateRefreshToken(ctx context.Context, token *models.CISRefreshToken) error {
	return r.db.WithContext(ctx).Create(token).Error
}

// FindRefreshToken looks up a stored token by its hash.
func (r *UserRepository) FindRefreshToken(ctx context.Context, hash string) (*models.CISRefreshToken, error) {
	var token models.CISRefreshToken
	err := r.db.WithContext(ctx).Where("token_hash = ?", hash).First(&token).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &token, nil
}

// RevokeRefreshToken marks a single token unusable.
func (r *UserRepository) RevokeRefreshToken(ctx context.Context, hash string, at time.Time) error {
	return r.db.WithContext(ctx).
		Model(&models.CISRefreshToken{}).
		Where("token_hash = ? AND revoked_at IS NULL", hash).
		Update("revoked_at", at).Error
}

// RevokeAllUserTokens revokes every active token for a user, backing logout.
func (r *UserRepository) RevokeAllUserTokens(ctx context.Context, userID uuid.UUID, at time.Time) error {
	return r.db.WithContext(ctx).
		Model(&models.CISRefreshToken{}).
		Where("user_id = ? AND revoked_at IS NULL", userID).
		Update("revoked_at", at).Error
}

// DeleteExpiredRefreshTokens prunes tokens that expired before the cutoff.
func (r *UserRepository) DeleteExpiredRefreshTokens(ctx context.Context, before time.Time) (int64, error) {
	res := r.db.WithContext(ctx).
		Where("expires_at < ?", before).
		Delete(&models.CISRefreshToken{})
	return res.RowsAffected, res.Error
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}
