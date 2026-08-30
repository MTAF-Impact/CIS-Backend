package handler

import (
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/cis/cis-backend/internal/dto"
	"github.com/cis/cis-backend/internal/middleware"
	"github.com/cis/cis-backend/internal/pkg/apperr"
	"github.com/cis/cis-backend/internal/pkg/response"
	"github.com/cis/cis-backend/internal/service"
)

// SettingHandler serves the F4 Admin Settings page.
type SettingHandler struct {
	settings *service.SettingService
}

// NewSettingHandler constructs a SettingHandler.
func NewSettingHandler(settings *service.SettingService) *SettingHandler {
	return &SettingHandler{settings: settings}
}

// UpdateThresholdRequest is the body of PUT /api/v1/settings/alert-threshold.
type UpdateThresholdRequest struct {
	Threshold *float64 `json:"threshold" validate:"required,gte=0,lte=100"`
}

// List handles GET /api/v1/settings.
func (h *SettingHandler) List(c *fiber.Ctx) error {
	settings, err := h.settings.List(c.UserContext())
	if err != nil {
		return err
	}
	return response.OK(c, "settings", settings)
}

// GetAlertThreshold handles GET /api/v1/settings/alert-threshold (US32).
func (h *SettingHandler) GetAlertThreshold(c *fiber.Ctx) error {
	threshold, err := h.settings.AlertThresholdView(c.UserContext())
	if err != nil {
		return err
	}
	return response.OK(c, "alert threshold", threshold)
}

// UpdateAlertThreshold handles PUT /api/v1/settings/alert-threshold (US32).
//
// The threshold is global: changing it immediately changes the Over/Under
// Threshold status of every claim on F3.
func (h *SettingHandler) UpdateAlertThreshold(c *fiber.Ctx) error {
	var req UpdateThresholdRequest
	if err := c.BodyParser(&req); err != nil {
		return apperr.BadRequest("request body must be valid JSON").Wrap(err)
	}
	if err := dto.Validate(req); err != nil {
		return err
	}

	threshold, err := h.settings.SetAlertThreshold(c.UserContext(), *req.Threshold, middleware.UserIDFromContext(c))
	if err != nil {
		return err
	}
	return response.OK(c, "alert threshold updated", threshold)
}

// AdminHandler serves the F4 MVP test utilities.
type AdminHandler struct {
	admin  *service.AdminService
	alerts *service.AlertService
}

// NewAdminHandler constructs an AdminHandler.
func NewAdminHandler(admin *service.AdminService, alerts *service.AlertService) *AdminHandler {
	return &AdminHandler{admin: admin, alerts: alerts}
}

// GenerateGenericClaimRequest optionally pins the generated claim to a topic.
type GenerateGenericClaimRequest struct {
	TopicID *string `json:"topic_id" validate:"omitempty,uuid"`
}

// GenerateGenericClaim handles POST /api/v1/admin/generate-generic-claim
// (US33).
func (h *AdminHandler) GenerateGenericClaim(c *fiber.Ctx) error {
	var req GenerateGenericClaimRequest
	// The body is optional, so a parse failure on an empty body is not fatal.
	if len(c.Body()) > 0 {
		if err := c.BodyParser(&req); err != nil {
			return apperr.BadRequest("request body must be valid JSON").Wrap(err)
		}
		if err := dto.Validate(req); err != nil {
			return err
		}
	}

	if req.TopicID != nil {
		trimmed := strings.TrimSpace(*req.TopicID)
		if _, err := uuid.Parse(trimmed); err != nil {
			return apperr.BadRequest("topic_id must be a valid UUID")
		}
		req.TopicID = &trimmed
	}

	res, err := h.admin.GenerateGenericClaim(c.UserContext(), req.TopicID, middleware.UserIDFromContext(c))
	if err != nil {
		return err
	}
	return response.Created(c, "generic claim generated", res)
}

// SnapshotScores handles POST /api/v1/admin/snapshot-scores, a manual trigger
// for the job that builds the F3 chart history.
func (h *AdminHandler) SnapshotScores(c *fiber.Ctx) error {
	count, err := h.alerts.CaptureSnapshots(c.UserContext())
	if err != nil {
		return err
	}
	return response.OK(c, "score snapshots captured", fiber.Map{"snapshots_captured": count})
}
