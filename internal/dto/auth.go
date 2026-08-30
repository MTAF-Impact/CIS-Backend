package dto

import "time"

// RegisterRequest is the body of POST /api/v1/auth/register.
type RegisterRequest struct {
	Email    string `json:"email" validate:"required,email,max=255"`
	Password string `json:"password" validate:"required"`
	Name     string `json:"name" validate:"required,min=2,max=255"`
}

// LoginRequest is the body of POST /api/v1/auth/login.
type LoginRequest struct {
	Email    string `json:"email" validate:"required,email"`
	Password string `json:"password" validate:"required"`
}

// RefreshRequest is the body of POST /api/v1/auth/refresh.
type RefreshRequest struct {
	RefreshToken string `json:"refresh_token" validate:"required"`
}

// UserResponse is the public view of an operator account. It never includes the
// password hash.
type UserResponse struct {
	ID          string     `json:"id"`
	Email       string     `json:"email"`
	Name        string     `json:"name"`
	LastLoginAt *time.Time `json:"last_login_at"`
	CreatedAt   time.Time  `json:"created_at"`
}

// AuthResponse is returned by register, login, and refresh.
type AuthResponse struct {
	User         UserResponse `json:"user"`
	AccessToken  string       `json:"access_token"`
	RefreshToken string       `json:"refresh_token"`
	TokenType    string       `json:"token_type"`
	ExpiresIn    int          `json:"expires_in"` // access token lifetime, seconds
}
