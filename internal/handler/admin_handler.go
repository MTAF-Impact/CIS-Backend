package handler

import (
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/cis/cis-backend/internal/dto"
	"github.com/cis/cis-backend/internal/middleware"
	"github.com/cis/cis-backend/internal/models"
	"github.com/cis/cis-backend/internal/pkg/apperr"
	"github.com/cis/cis-backend/internal/pkg/response"
	"github.com/cis/cis-backend/internal/service"
)

// SettingHandler serves the Admin Settings page.
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

// GetAlertThreshold handles GET /api/v1/settings/alert-threshold.
func (h *SettingHandler) GetAlertThreshold(c *fiber.Ctx) error {
	threshold, err := h.settings.AlertThresholdView(c.UserContext())
	if err != nil {
		return err
	}
	return response.OK(c, "alert threshold", threshold)
}

// UpdateAlertThreshold handles PUT /api/v1/settings/alert-threshold.
//
// The threshold is global: changing it immediately changes the Over/Under
// Threshold status of every claim on the Alert page.
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

// SetCityRequest is the body of PUT /api/v1/settings/city.
type SetCityRequest struct {
	City string `json:"city" validate:"required"`
}

// Cities handles GET /api/v1/settings/cities, the dropdown options.
func (h *SettingHandler) Cities(c *fiber.Ctx) error {
	return response.OK(c, "configurable cities", fiber.Map{
		"cities":   models.IndonesianCities,
		"selected": h.settings.MonitoredCity(c.UserContext()),
	})
}

// GetCity handles GET /api/v1/settings/city.
func (h *SettingHandler) GetCity(c *fiber.Ctx) error {
	return response.OK(c, "monitored city", h.settings.MonitoredCity(c.UserContext()))
}

// SetCity handles PUT /api/v1/settings/city.
//
// Single-select: the new city replaces the previous one outright, since this
// phase has no concurrent multi-city state. Every Overview metric re-scopes
// on the next request, and the report footer picks up the new city-local
// timezone.
func (h *SettingHandler) SetCity(c *fiber.Ctx) error {
	var req SetCityRequest
	if err := c.BodyParser(&req); err != nil {
		return apperr.BadRequest("request body must be valid JSON").Wrap(err)
	}
	if err := dto.Validate(req); err != nil {
		return err
	}

	city, err := h.settings.SetMonitoredCity(c.UserContext(), req.City, middleware.UserIDFromContext(c))
	if err != nil {
		return err
	}
	return response.OK(c, "monitored city updated", city)
}

// Parameters handles GET /api/v1/settings/parameters.
//
// The whole dynamic-parameter surface in one call: two tiers, the sections
// inside them, and every parameter's definition alongside its current value.
// The Admin Settings form renders from this rather than from a second copy
// of the specification, which is what stops a bound in the form and the
// bound the server enforces from drifting apart.
func (h *SettingHandler) Parameters(c *fiber.Ctx) error {
	return response.OK(c, "configurable parameters", h.settings.ConfigCatalog(c.UserContext()))
}

// UpdateParameters handles PUT /api/v1/settings/parameters.
//
// A partial update: only the keys present in the body change, and the rest keep
// their stored values. The response is the full refreshed catalog, so a form
// that saves one weight can re-render the running totals of the group it
// belongs to without a second request.
//
// A 422 carries per-key messages. Two shapes reach it: a value outside its own
// bounds, keyed by the parameter; and a set that is individually valid but
// inconsistent — the five composite weights no longer summing to 1.00, say —
// keyed by the group's name.
func (h *SettingHandler) UpdateParameters(c *fiber.Ctx) error {
	var req dto.UpdateConfigParamsRequest
	if err := c.BodyParser(&req); err != nil {
		return apperr.BadRequest("request body must be valid JSON").Wrap(err)
	}
	if err := dto.Validate(req); err != nil {
		return err
	}

	catalog, err := h.settings.UpdateConfigParams(
		c.UserContext(), req.Parameters, middleware.UserIDFromContext(c))
	if err != nil {
		return err
	}
	return response.OK(c, "configuration updated", catalog)
}

// ResetParameter handles DELETE /api/v1/settings/parameters/:key.
//
// Restores one parameter to its documented default by removing the stored row,
// rather than by writing the default back as a value. The distinction matters:
// a parameter with no row follows the specification if the specification is
// ever revised, while one holding a copy of yesterday's default silently would
// not.
func (h *SettingHandler) ResetParameter(c *fiber.Ctx) error {
	key := strings.TrimSpace(c.Params("key"))
	if key == "" {
		return apperr.BadRequest("a parameter key is required")
	}

	if err := h.settings.ResetConfigParam(c.UserContext(), key, middleware.UserIDFromContext(c)); err != nil {
		return err
	}
	return response.OK(c, "parameter reset to its default", h.settings.ConfigCatalog(c.UserContext()))
}

// AdminHandler serves the MVP test utilities.
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

// GenerateGenericClaim handles POST /api/v1/admin/generate-generic-claim.
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
// for the job that builds the Alert page's chart history.
func (h *AdminHandler) SnapshotScores(c *fiber.Ctx) error {
	count, err := h.alerts.CaptureSnapshots(c.UserContext())
	if err != nil {
		return err
	}
	return response.OK(c, "score snapshots captured", fiber.Map{"snapshots_captured": count})
}

// GenerateSampleContentRequest is the body of
// POST /api/v1/admin/generate-sample-content. Every field is optional; the AI
// service applies its own defaults (10 items, auto-clustered).
type GenerateSampleContentRequest struct {
	Count       int     `json:"count" validate:"omitempty,gte=1,lte=50"`
	TopicHint   *string `json:"topic_hint" validate:"omitempty,max=255"`
	AutoCluster *bool   `json:"auto_cluster"`
}

// GenerateSampleContent handles POST /api/v1/admin/generate-sample-content.
//
// Long-running: with auto_cluster left at its default the AI service clusters
// synchronously before replying, so this runs on AI_SERVICE_LONG_TIMEOUT.
func (h *AdminHandler) GenerateSampleContent(c *fiber.Ctx) error {
	var req GenerateSampleContentRequest
	// The body is optional, so a parse failure on an empty body is not fatal.
	if len(c.Body()) > 0 {
		if err := c.BodyParser(&req); err != nil {
			return apperr.BadRequest("request body must be valid JSON").Wrap(err)
		}
		if err := dto.Validate(req); err != nil {
			return err
		}
	}

	res, err := h.admin.GenerateSampleContent(c.UserContext(), service.GenerateSampleContentInput{
		Count:       req.Count,
		TopicHint:   req.TopicHint,
		AutoCluster: req.AutoCluster,
	}, middleware.UserIDFromContext(c))
	if err != nil {
		return err
	}
	return response.Created(c, "sample content generated", res)
}

// ClusterNow handles POST /api/v1/admin/cluster-now, forcing a clustering pass
// over content the AI service has ingested but not yet grouped into claims.
func (h *AdminHandler) ClusterNow(c *fiber.Ctx) error {
	res, err := h.admin.ClusterNow(c.UserContext())
	if err != nil {
		return err
	}
	return response.OK(c, "clustering pass complete", res)
}

// Rescore handles POST /api/v1/admin/rescore, the manual trigger for the
// time-based score re-evaluation the snapshot cron also runs.
func (h *AdminHandler) Rescore(c *fiber.Ctx) error {
	res, err := h.admin.Rescore(c.UserContext())
	if err != nil {
		return err
	}
	return response.OK(c, "claims rescored", res)
}

// ReconcileRequest is the body of POST /api/v1/admin/reconcile.
type ReconcileRequest struct {
	// DryRun reports what would be cleared without clearing it.
	DryRun bool `json:"dry_run"`
	// Force overrides the guard that refuses to sweep when the AI service's
	// claims table is empty — which normally means a misconfigured database
	// rather than a deliberate wipe.
	Force bool `json:"force"`
}

// Reconcile handles POST /api/v1/admin/reconcile, clearing backend rows whose
// AI-side claim or policy no longer exists.
func (h *AdminHandler) Reconcile(c *fiber.Ctx) error {
	var req ReconcileRequest
	if len(c.Body()) > 0 {
		if err := c.BodyParser(&req); err != nil {
			return apperr.BadRequest("request body must be valid JSON").Wrap(err)
		}
	}

	res, err := h.admin.Reconcile(c.UserContext(), req.DryRun, req.Force)
	if err != nil {
		return err
	}
	return response.OK(c, res.Message, res)
}
