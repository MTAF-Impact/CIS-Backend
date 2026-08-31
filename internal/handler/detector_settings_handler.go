package handler

import (
	"github.com/gofiber/fiber/v2"

	"github.com/cis/cis-backend/internal/dto"
	"github.com/cis/cis-backend/internal/middleware"
	"github.com/cis/cis-backend/internal/pkg/apperr"
	"github.com/cis/cis-backend/internal/pkg/response"
	"github.com/cis/cis-backend/internal/repository"
	"github.com/cis/cis-backend/internal/service"
)

// DetectorSettingsHandler serves the F5 half of the F4 Admin Settings page:
// the governed detector parameters (US62) and their change history.
//
// Separate from SettingHandler because it needs the allowlist service too — the
// self-exclusion account list US62 names is managed as an allowlist category,
// and the settings screen has to show how many accounts are in it.
type DetectorSettingsHandler struct {
	settings  *service.SettingService
	allowlist *service.AllowlistService
}

// NewDetectorSettingsHandler constructs a DetectorSettingsHandler.
func NewDetectorSettingsHandler(
	settings *service.SettingService, allowlist *service.AllowlistService,
) *DetectorSettingsHandler {
	return &DetectorSettingsHandler{settings: settings, allowlist: allowlist}
}

// Get handles GET /api/v1/settings/detector (US62).
func (h *DetectorSettingsHandler) Get(c *fiber.Ctx) error {
	view, err := h.settings.DetectorSettingsView(c.UserContext(), h.allowlist.SelfExclusionCount(c.UserContext()))
	if err != nil {
		return err
	}
	return response.OK(c, "detector settings", view)
}

// Update handles PUT /api/v1/settings/detector (US62).
//
// Every field is optional; an omitted parameter keeps its stored value. Range
// and cross-field validation happens server-side, so a client that skips the
// bounds from GET /settings/detector/ranges gets a 422 naming the offending
// fields rather than a silently accepted configuration.
func (h *DetectorSettingsHandler) Update(c *fiber.Ctx) error {
	var req dto.UpdateDetectorSettingsRequest
	if err := c.BodyParser(&req); err != nil {
		return apperr.BadRequest("request body must be valid JSON").Wrap(err)
	}

	view, err := h.settings.UpdateDetectorSettings(c.UserContext(), req, middleware.UserIDFromContext(c))
	if err != nil {
		return err
	}
	view.SelfExclusionCount = h.allowlist.SelfExclusionCount(c.UserContext())
	return response.OK(c, "detector settings updated", view)
}

// Ranges handles GET /api/v1/settings/detector/ranges.
//
// PRD 10.11's Default Parameter Reference, served so the F4 screen can render
// bounded inputs without a second copy of the table. Two copies of a
// specification drift, and here the drift would be a form that happily accepts
// a value the server then rejects.
func (h *DetectorSettingsHandler) Ranges(c *fiber.Ctx) error {
	return response.OK(c, "detector parameter ranges (PRD 10.11)", h.settings.DetectorParamRanges())
}

// History handles GET /api/v1/settings/detector/history (US62).
//
// US62 requires every change versioned with the acting user and a timestamp.
// The `key` query narrows to one parameter; omitting it returns every detector
// change.
func (h *DetectorSettingsHandler) History(c *fiber.Ctx) error {
	prefix := repository.DetectorSettingPrefix
	if key := c.Query("key"); key != "" {
		prefix += key
	}

	entries, total, page, err := h.settings.SettingHistory(
		c.UserContext(), prefix, c.QueryInt("page", dto.DefaultPage), c.QueryInt("limit", dto.DefaultLimit))
	if err != nil {
		return err
	}
	return response.List(c, "detector settings history", entries, response.NewMeta(page.Page, page.Limit, total))
}

// AllHistory handles GET /api/v1/settings/history.
//
// The whole F4 surface, not only the detector: the alert threshold (US32) and
// the city timezone are versioned by the same mechanism. US62 demands it only
// for the detector, but the threshold has been globally governed since v1.3
// with no record of who moved it.
func (h *DetectorSettingsHandler) AllHistory(c *fiber.Ctx) error {
	entries, total, page, err := h.settings.SettingHistory(
		c.UserContext(), c.Query("key"), c.QueryInt("page", dto.DefaultPage), c.QueryInt("limit", dto.DefaultLimit))
	if err != nil {
		return err
	}
	return response.List(c, "settings history", entries, response.NewMeta(page.Page, page.Limit, total))
}

// SetCityTimezoneRequest is the body of PUT /api/v1/settings/city-timezone.
type SetCityTimezoneRequest struct {
	Timezone string `json:"timezone" validate:"required,max=64"`
}

// CityTimezone handles GET /api/v1/settings/city-timezone (PRD 10.8).
func (h *DetectorSettingsHandler) CityTimezone(c *fiber.Ctx) error {
	loc := h.settings.CityTimezone(c.UserContext())
	return response.OK(c, "city timezone", fiber.Map{
		"timezone": loc.String(),
		"note": "Used for the city-local half of every F5 report footer timestamp. PRD 10.8 requires both " +
			"UTC and city-local time on every page but does not name the city.",
	})
}

// SetCityTimezone handles PUT /api/v1/settings/city-timezone (PRD 10.8).
func (h *DetectorSettingsHandler) SetCityTimezone(c *fiber.Ctx) error {
	var req SetCityTimezoneRequest
	if err := c.BodyParser(&req); err != nil {
		return apperr.BadRequest("request body must be valid JSON").Wrap(err)
	}
	if err := dto.Validate(req); err != nil {
		return err
	}

	name, err := h.settings.SetCityTimezone(c.UserContext(), req.Timezone, middleware.UserIDFromContext(c))
	if err != nil {
		return err
	}
	return response.OK(c, "city timezone updated", fiber.Map{"timezone": name})
}
