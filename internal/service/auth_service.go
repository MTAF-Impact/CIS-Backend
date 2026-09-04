// Package service holds the application's business logic. Services depend on
// repositories and never on Fiber, so they stay independently testable.
package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/cis/cis-backend/internal/config"
	"github.com/cis/cis-backend/internal/dto"
	"github.com/cis/cis-backend/internal/models"
	"github.com/cis/cis-backend/internal/pkg/apperr"
	"github.com/cis/cis-backend/internal/repository"
)

// AuthService implements the login flow: registration, password verification,
// JWT issuance, and refresh-token rotation.
//
// There are no roles by design — any authenticated user may call every
// endpoint, including the admin settings.
type AuthService struct {
	users *repository.UserRepository
	cfg   config.AuthConfig
}

// NewAuthService constructs an AuthService.
func NewAuthService(users *repository.UserRepository, cfg config.AuthConfig) *AuthService {
	return &AuthService{users: users, cfg: cfg}
}

// AccessClaims is the JWT payload carried by access tokens.
type AccessClaims struct {
	UserID string `json:"uid"`
	Email  string `json:"email"`
	Name   string `json:"name"`
	jwt.RegisteredClaims
}

// Register creates a new operator account and immediately signs it in.
func (s *AuthService) Register(ctx context.Context, req dto.RegisterRequest) (*dto.AuthResponse, error) {
	if !s.cfg.AllowRegistration {
		return nil, apperr.Forbidden("self-registration is disabled; ask an operator to create your account")
	}

	email := strings.ToLower(strings.TrimSpace(req.Email))
	exists, err := s.users.EmailExists(ctx, email)
	if err != nil {
		return nil, apperr.Internal("could not verify email availability").Wrap(err)
	}
	if exists {
		return nil, apperr.Conflict("an account with this email already exists")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), s.cfg.BcryptCost)
	if err != nil {
		return nil, apperr.Internal("could not hash password").Wrap(err)
	}

	user := &models.CISUser{
		Email:        email,
		PasswordHash: string(hash),
		Name:         strings.TrimSpace(req.Name),
	}
	if err := s.users.CreateUser(ctx, user); err != nil {
		return nil, apperr.Internal("could not create account").Wrap(err)
	}

	return s.issueTokens(ctx, user)
}

// Login verifies credentials and issues a token pair.
func (s *AuthService) Login(ctx context.Context, req dto.LoginRequest) (*dto.AuthResponse, error) {
	email := strings.ToLower(strings.TrimSpace(req.Email))

	user, err := s.users.FindUserByEmail(ctx, req.Email)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			// Compare against a dummy hash anyway so response time does not
			// reveal whether the email exists.
			_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(req.Password))
			log.Printf("[auth] login failed: no account for %q", email)
			return nil, apperr.Unauthorized("invalid email or password")
		}
		return nil, apperr.Internal("could not look up account").Wrap(err)
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		log.Printf("[auth] login failed: wrong password for %q (user %s)", email, user.ID)
		return nil, apperr.Unauthorized("invalid email or password")
	}

	now := time.Now().UTC()
	if err := s.users.TouchLastLogin(ctx, user.ID, now); err != nil {
		return nil, apperr.Internal("could not record login").Wrap(err)
	}
	user.LastLoginAt = &now

	return s.issueTokens(ctx, user)
}

// Refresh rotates a refresh token, revoking the presented one and issuing a
// fresh pair. A reused or expired token is rejected.
func (s *AuthService) Refresh(ctx context.Context, raw string) (*dto.AuthResponse, error) {
	hash := hashToken(raw)

	stored, err := s.users.FindRefreshToken(ctx, hash)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			log.Print("[auth] refresh rejected: unknown token")
			return nil, apperr.Unauthorized("invalid refresh token")
		}
		return nil, apperr.Internal("could not look up refresh token").Wrap(err)
	}

	now := time.Now().UTC()
	if !stored.IsUsable(now) {
		// A revoked-but-present token is a rotated one being replayed — worth a
		// line on its own, since it can mean the token was stolen.
		log.Printf("[auth] refresh rejected: token expired or revoked (user %s)", stored.UserID)
		return nil, apperr.Unauthorized("refresh token is expired or revoked")
	}

	user, err := s.users.FindUserByID(ctx, stored.UserID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, apperr.Unauthorized("account no longer exists")
		}
		return nil, apperr.Internal("could not look up account").Wrap(err)
	}

	// Rotate: the presented token is single-use.
	if err := s.users.RevokeRefreshToken(ctx, hash, now); err != nil {
		return nil, apperr.Internal("could not rotate refresh token").Wrap(err)
	}

	return s.issueTokens(ctx, user)
}

// Logout revokes every refresh token belonging to the user. Access tokens
// remain valid until they expire, which is why the access TTL is kept short.
func (s *AuthService) Logout(ctx context.Context, userID uuid.UUID) error {
	if err := s.users.RevokeAllUserTokens(ctx, userID, time.Now().UTC()); err != nil {
		return apperr.Internal("could not revoke sessions").Wrap(err)
	}
	return nil
}

// Me returns the profile of the authenticated user.
func (s *AuthService) Me(ctx context.Context, userID uuid.UUID) (*dto.UserResponse, error) {
	user, err := s.users.FindUserByID(ctx, userID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, apperr.NotFound("account not found")
		}
		return nil, apperr.Internal("could not look up account").Wrap(err)
	}
	view := toUserResponse(user)
	return &view, nil
}

// ParseAccessToken validates an access token's signature and claims. Used by
// the auth middleware.
func (s *AuthService) ParseAccessToken(raw string) (*AccessClaims, error) {
	token, err := jwt.ParseWithClaims(raw, &AccessClaims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return []byte(s.cfg.JWTSecret), nil
	}, jwt.WithIssuer(s.cfg.Issuer), jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil {
		return nil, apperr.Unauthorized("invalid or expired access token").Wrap(err)
	}

	claims, ok := token.Claims.(*AccessClaims)
	if !ok || !token.Valid {
		return nil, apperr.Unauthorized("invalid access token")
	}
	return claims, nil
}

func (s *AuthService) issueTokens(ctx context.Context, user *models.CISUser) (*dto.AuthResponse, error) {
	now := time.Now().UTC()

	accessToken, err := s.signAccessToken(user, now)
	if err != nil {
		return nil, err
	}

	rawRefresh, err := randomToken()
	if err != nil {
		return nil, apperr.Internal("could not generate refresh token").Wrap(err)
	}

	record := &models.CISRefreshToken{
		UserID:    user.ID,
		TokenHash: hashToken(rawRefresh),
		ExpiresAt: now.Add(s.cfg.RefreshTokenTTL),
	}
	if err := s.users.CreateRefreshToken(ctx, record); err != nil {
		return nil, apperr.Internal("could not persist refresh token").Wrap(err)
	}

	return &dto.AuthResponse{
		User:         toUserResponse(user),
		AccessToken:  accessToken,
		RefreshToken: rawRefresh,
		TokenType:    "Bearer",
		ExpiresIn:    int(s.cfg.AccessTokenTTL.Seconds()),
	}, nil
}

func (s *AuthService) signAccessToken(user *models.CISUser, now time.Time) (string, error) {
	claims := AccessClaims{
		UserID: user.ID.String(),
		Email:  user.Email,
		Name:   user.Name,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   user.ID.String(),
			Issuer:    s.cfg.Issuer,
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(s.cfg.AccessTokenTTL)),
			ID:        uuid.NewString(),
		},
	}

	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(s.cfg.JWTSecret))
	if err != nil {
		return "", apperr.Internal("could not sign access token").Wrap(err)
	}
	return signed, nil
}

func toUserResponse(u *models.CISUser) dto.UserResponse {
	return dto.UserResponse{
		ID:          u.ID.String(),
		Email:       u.Email,
		Name:        u.Name,
		LastLoginAt: u.LastLoginAt,
		CreatedAt:   u.CreatedAt,
	}
}

// randomToken produces a 256-bit URL-safe opaque refresh token.
func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// hashToken derives the value stored in the database. Refresh tokens are
// high-entropy random strings, so a plain SHA-256 is sufficient here — unlike
// passwords, they are not guessable and need no work factor.
func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// dummyHash is a valid bcrypt hash of a random value, compared against when an
// email is not found to keep login timing uniform.
var dummyHash = []byte("$2a$12$C6UzMDM.H6dfI/f/IKcEe.7iCE2QHzB5F0/xJDNGKDsQqPzPvS2ii")
