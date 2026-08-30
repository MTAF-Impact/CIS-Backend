// Package handler contains the Fiber HTTP handlers. Handlers parse and
// validate input, delegate to a service, and shape the response — they never
// touch GORM directly.
package handler

import (
	"github.com/gofiber/fiber/v2"

	"github.com/cis/cis-backend/internal/dto"
	"github.com/cis/cis-backend/internal/middleware"
	"github.com/cis/cis-backend/internal/pkg/apperr"
	"github.com/cis/cis-backend/internal/pkg/response"
	"github.com/cis/cis-backend/internal/service"
)

// AuthHandler serves the login flow.
type AuthHandler struct {
	auth *service.AuthService
}

// NewAuthHandler constructs an AuthHandler.
func NewAuthHandler(auth *service.AuthService) *AuthHandler {
	return &AuthHandler{auth: auth}
}

// Register handles POST /api/v1/auth/register.
func (h *AuthHandler) Register(c *fiber.Ctx) error {
	var req dto.RegisterRequest
	if err := c.BodyParser(&req); err != nil {
		return apperr.BadRequest("request body must be valid JSON").Wrap(err)
	}
	if err := dto.Validate(req); err != nil {
		return err
	}

	res, err := h.auth.Register(c.UserContext(), req)
	if err != nil {
		return err
	}
	return response.Created(c, "account created", res)
}

// Login handles POST /api/v1/auth/login.
func (h *AuthHandler) Login(c *fiber.Ctx) error {
	var req dto.LoginRequest
	if err := c.BodyParser(&req); err != nil {
		return apperr.BadRequest("request body must be valid JSON").Wrap(err)
	}
	if err := dto.Validate(req); err != nil {
		return err
	}

	res, err := h.auth.Login(c.UserContext(), req)
	if err != nil {
		return err
	}
	return response.OK(c, "signed in", res)
}

// Refresh handles POST /api/v1/auth/refresh.
func (h *AuthHandler) Refresh(c *fiber.Ctx) error {
	var req dto.RefreshRequest
	if err := c.BodyParser(&req); err != nil {
		return apperr.BadRequest("request body must be valid JSON").Wrap(err)
	}
	if err := dto.Validate(req); err != nil {
		return err
	}

	res, err := h.auth.Refresh(c.UserContext(), req.RefreshToken)
	if err != nil {
		return err
	}
	return response.OK(c, "token refreshed", res)
}

// Logout handles POST /api/v1/auth/logout.
func (h *AuthHandler) Logout(c *fiber.Ctx) error {
	userID, err := middleware.MustUserID(c)
	if err != nil {
		return err
	}
	if err := h.auth.Logout(c.UserContext(), userID); err != nil {
		return err
	}
	return response.OK(c, "signed out", nil)
}

// Me handles GET /api/v1/auth/me.
func (h *AuthHandler) Me(c *fiber.Ctx) error {
	userID, err := middleware.MustUserID(c)
	if err != nil {
		return err
	}

	user, err := h.auth.Me(c.UserContext(), userID)
	if err != nil {
		return err
	}
	return response.OK(c, "current user", user)
}
